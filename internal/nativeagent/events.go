package nativeagent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
)

// DecodeEvents validates one completed native text-only turn and its reported
// token usage without performing inference. It can also validate separately
// authorized Codex baseline receipts. It does not establish model identity,
// completeness of a captured stream, or the number of underlying HTTP calls.
func DecodeEvents(data []byte, model string) (inference.Response, error) {
	return parseEvents(data, model)
}

// Native events contain forward-compatible mixed-case nested fields. Do not
// decode them using the broker's narrower lowercase synchronization protocol.
func parseEvents(data []byte, model string) (inference.Response, error) {
	result := inference.Response{Model: model, StopReason: "native_output_invalid"}
	fail := func() (inference.Response, error) {
		result.Complete = false
		return result, errors.New("native client returned incomplete or invalid event output")
	}
	if len(data) == 0 || len(data) > maxOutputBytes {
		return fail()
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	starts, completions, threads := 0, 0, 0
	final := ""
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > maxLineBytes {
			return fail()
		}
		if err := validateEventJSON(line); err != nil {
			return fail()
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil {
			return fail()
		}
		typ, ok := requiredString(event, "type")
		if !ok {
			return fail()
		}
		// A single requested role cannot turn into additional native turns or
		// append a new answer after its completion receipt.
		if completions > 0 {
			return fail()
		}
		switch typ {
		case "thread.started":
			threads++
			id, ok := requiredString(event, "thread_id")
			if !ok || threads != 1 || starts != 0 {
				return fail()
			}
			result.ID = id
		case "turn.started":
			starts++
			if starts != 1 || threads != 1 {
				return fail()
			}
		case "turn.completed":
			completions++
			if starts != 1 || final == "" {
				return fail()
			}
			usage, err := parseUsage(event["usage"])
			if err != nil {
				return fail()
			}
			result.Usage = usage
		case "turn.failed", "error":
			return fail()
		case "item.started", "item.updated", "item.completed":
			if starts != 1 {
				return fail()
			}
			var item map[string]json.RawMessage
			if json.Unmarshal(event["item"], &item) != nil || item == nil {
				return fail()
			}
			itemType, ok := requiredString(item, "type")
			if !ok {
				return fail()
			}
			// These roles have no tools. An unexpected tool item is never
			// treated as an acceptable text-only answer, even on exit code 0.
			if itemType != "agent_message" && itemType != "reasoning" {
				return fail()
			}
			if typ == "item.completed" && itemType == "agent_message" {
				text, ok := requiredString(item, "text")
				if !ok || strings.TrimSpace(text) == "" {
					return fail()
				}
				if status, present := item["status"]; present {
					var s string
					if json.Unmarshal(status, &s) != nil || s != "completed" {
						return fail()
					}
				}
				if phase, present := item["phase"]; present {
					var value string
					if json.Unmarshal(phase, &value) != nil {
						return fail()
					}
					if value == "commentary" {
						continue
					}
					if value != "final_answer" {
						return fail()
					}
				}
				final = text
			}
		default:
			// Unknown event types can alter execution semantics. Unknown
			// fields inside known events remain forward-compatible.
			return fail()
		}
	}
	if scanner.Err() != nil || threads != 1 || starts != 1 || completions != 1 || final == "" {
		return fail()
	}
	result.Text = final
	result.Complete = true
	result.StopReason = "completed"
	return result, nil
}

func requiredString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || text == "" {
		return "", false
	}
	return text, true
}

func parseUsage(data []byte) (inference.Usage, error) {
	var raw map[string]json.RawMessage
	fail := errors.New("invalid native usage")
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return inference.Usage{}, fail
	}
	keys := []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "reasoning_tokens"}
	for actual := range raw {
		for _, key := range keys {
			if actual != key && strings.EqualFold(actual, key) {
				return inference.Usage{}, fail
			}
		}
	}
	values := make([]int64, 3)
	for i, key := range keys[:3] {
		var value *int64
		if json.Unmarshal(raw[key], &value) != nil || value == nil || *value < 0 || *value > 1_000_000_000 {
			return inference.Usage{}, fail
		}
		values[i] = *value
	}
	if values[1] > values[0] {
		return inference.Usage{}, fail
	}
	var reasoning int64
	if raw["reasoning_output_tokens"] != nil && raw["reasoning_tokens"] != nil {
		return inference.Usage{}, fail
	}
	for _, value := range []json.RawMessage{raw["reasoning_output_tokens"], raw["reasoning_tokens"]} {
		if value == nil {
			continue
		}
		var n *int64
		if json.Unmarshal(value, &n) != nil || n == nil || *n < 0 || *n > 1_000_000_000 || *n > values[2] {
			return inference.Usage{}, fail
		}
		reasoning = *n
	}
	return inference.Usage{InputTokens: values[0], UncachedInputTokens: values[0] - values[1], CacheReadTokens: values[1], OutputTokens: values[2], ReasoningTokens: reasoning}, nil
}

func validateEventJSON(data []byte) error {
	if !utf8.Valid(data) || !validEscapes(data) {
		return errors.New("invalid JSON Unicode")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("event is not an object")
	}
	if err = walkJSON(d, token, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func walkJSON(d *json.Decoder, token json.Token, depth int) error {
	if depth > 32 {
		return errors.New("event nesting exceeds limit")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	names := map[string]bool{}
	for d.More() {
		if delim == '{' {
			name, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok || names[key] {
				return errors.New("duplicate event field")
			}
			names[key] = true
		}
		next, err := d.Token()
		if err != nil {
			return err
		}
		if err = walkJSON(d, next, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errors.New("invalid event structure")
	}
	return nil
}

func validEscapes(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' || i+4 >= len(data) {
			continue
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
