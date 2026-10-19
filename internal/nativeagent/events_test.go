package nativeagent

import (
	"strings"
	"testing"
)

const validEvents = `{"type":"thread.started","thread_id":"test-thread","futureMixedCase":{"UnknownField":true}}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"Private reasoning is not the final output."}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Synthetic approved answer.","futureMixedCase":null}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":30,"output_tokens":20}}
`

func TestNativeEventsRequireCompletedAnswerAndUsage(t *testing.T) {
	r, err := parseEvents([]byte(validEvents), "qwen3.5:4b")
	if err != nil || !r.Complete || r.Text != "Synthetic approved answer." || r.ID != "test-thread" || r.Usage.InputTokens != 100 || r.Usage.UncachedInputTokens != 70 || r.Usage.CacheReadTokens != 30 || r.Usage.OutputTokens != 20 {
		t.Fatalf("response=%+v err=%v", r, err)
	}
	withReasoning := strings.Replace(validEvents, `"output_tokens":20`, `"output_tokens":20,"reasoning_output_tokens":7`, 1)
	r, err = parseEvents([]byte(withReasoning), "qwen3.5:4b")
	if err != nil || r.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning=%+v %v", r, err)
	}
}

func TestNativeRecognizesOnlyEstablishedNonfatalStartupWarnings(t *testing.T) {
	const feature = `{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Under-development features enabled: skip_host_skill_discovery. Under-development features are incomplete and may behave unpredictably. To suppress this warning, set ` + "`suppress_unstable_features_warning = true`" + ` in /tmp/questlock-native-fixture/codex/config.toml."}}` + "\n"
	const metadata = `{"type":"item.completed","item":{"id":"item_1","type":"error","message":"Model metadata for ` + "`qwen3.5:4b`" + ` not found. Defaulting to fallback metadata; this can degrade performance and cause issues."}}` + "\n"
	withWarnings := strings.Replace(validEvents, `{"type":"turn.started"}`, feature+metadata+`{"type":"turn.started"}`, 1)
	response, err := DecodeEvents([]byte(withWarnings), "qwen3.5:4b")
	if err != nil || !response.Complete || response.StopReason != "completed_with_startup_warnings" || response.Usage.InputTokens != 100 {
		t.Fatalf("startup warnings rejected: %+v %v", response, err)
	}
	for name, stream := range map[string]string{
		"unknown startup error":     strings.Replace(withWarnings, "Model metadata for", "Cannot connect: Model metadata for", 1),
		"wrong model warning":       strings.Replace(withWarnings, "`qwen3.5:4b`", "`different:4b`", 1),
		"warning during turn":       strings.Replace(validEvents, `{"type":"turn.started"}`, `{"type":"turn.started"}`+"\n"+metadata, 1),
		"top-level error":           strings.Replace(withWarnings, `"type":"item.completed","item":{"id":"item_1","type":"error"`, `"type":"error","item":{"id":"item_1","type":"error"`, 1),
		"too many startup warnings": strings.Replace(withWarnings, `{"type":"turn.started"}`, metadata+`{"type":"turn.started"}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if r, err := DecodeEvents([]byte(stream), "qwen3.5:4b"); err == nil || r.Complete {
				t.Fatal("unexpected error accepted")
			}
		})
	}
}

func TestNativeCacheWriteUsageIsValidatedAndPartitioned(t *testing.T) {
	stream := strings.Replace(validEvents, `"cached_input_tokens":30`, `"cached_input_tokens":30,"cache_write_input_tokens":10`, 1)
	r, err := DecodeEvents([]byte(stream), "qwen3.5:4b")
	if err != nil || r.Usage.UncachedInputTokens != 60 || r.Usage.CacheReadTokens != 30 || r.Usage.CacheWriteTokens != 10 {
		t.Fatalf("cache partition: %+v %v", r, err)
	}
	for _, count := range []string{"null", "-1", "71", "1000000001", "0.5"} {
		stream := strings.Replace(validEvents, `"cached_input_tokens":30`, `"cached_input_tokens":30,"cache_write_input_tokens":`+count, 1)
		if r, err := DecodeEvents([]byte(stream), "qwen3.5:4b"); err == nil || r.Complete {
			t.Fatalf("invalid cache write usage accepted: %s", count)
		}
	}
}

func TestNativeEventsRejectUncertainResults(t *testing.T) {
	cases := map[string]string{
		"missing final":            strings.Replace(validEvents, `"type":"agent_message"`, `"type":"reasoning"`, 1),
		"blank final":              strings.Replace(validEvents, "Synthetic approved answer.", "   ", 1),
		"missing complete":         strings.Split(validEvents, `{"type":"turn.completed"`)[0],
		"multiple completion":      validEvents + `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
		"multiple turns":           strings.Replace(validEvents, `{"type":"turn.started"}`, "{\"type\":\"turn.started\"}\n{\"type\":\"turn.started\"}", 1),
		"failed":                   strings.Replace(validEvents, `"type":"turn.completed"`, `"type":"turn.failed"`, 1),
		"error":                    strings.Replace(validEvents, `"type":"turn.completed"`, `"type":"error"`, 1),
		"tool item":                strings.Replace(validEvents, `"type":"reasoning"`, `"type":"command_execution"`, 1),
		"item null":                strings.Replace(validEvents, `{"type":"turn.started"}`, "{\"type\":\"turn.started\"}\n{\"type\":\"item.completed\",\"item\":null}", 1),
		"missing usage":            strings.Replace(validEvents, `"usage":`, `"unknownUsage":`, 1),
		"null usage":               strings.Replace(validEvents, `{"input_tokens":100,"cached_input_tokens":30,"output_tokens":20}`, `null`, 1),
		"null input":               strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":null`, 1),
		"null cached":              strings.Replace(validEvents, `"cached_input_tokens":30`, `"cached_input_tokens":null`, 1),
		"null output":              strings.Replace(validEvents, `"output_tokens":20`, `"output_tokens":null`, 1),
		"negative":                 strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":-1`, 1),
		"fractional":               strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":100.5`, 1),
		"large":                    strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":1000000001`, 1),
		"cached exceeds input":     strings.Replace(validEvents, `"cached_input_tokens":30`, `"cached_input_tokens":101`, 1),
		"reasoning exceeds output": strings.Replace(validEvents, `"output_tokens":20`, `"output_tokens":20,"reasoning_output_tokens":21`, 1),
		"null reasoning":           strings.Replace(validEvents, `"output_tokens":20`, `"output_tokens":20,"reasoning_output_tokens":null`, 1),
		"dual reasoning fields":    strings.Replace(validEvents, `"output_tokens":20`, `"output_tokens":20,"reasoning_tokens":1,"reasoning_output_tokens":1`, 1),
		"duplicate critical":       strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":100,"input_tokens":1`, 1),
		"case alias critical":      strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":100,"INPUT_TOKENS":1`, 1),
		"duplicate escaped":        strings.Replace(validEvents, `"input_tokens":100`, `"input_tokens":100,"input_\u0074okens":1`, 1),
		"duplicate type":           strings.Replace(validEvents, `"type":"turn.started"`, `"type":"turn.started","type":"error"`, 1),
		"null type":                strings.Replace(validEvents, `"type":"turn.started"`, `"type":null`, 1),
		"unsupported event":        validEvents + `{"type":"new.unknown"}`,
		"unpaired surrogate":       strings.Replace(validEvents, "Synthetic approved answer.", `\ud800`, 1),
		"invalid UTF8":             strings.Replace(validEvents, "Synthetic approved answer.", "\xff", 1),
		"trailing garbage":         validEvents + "not JSON\n",
		"trailing object":          strings.Replace(validEvents, `{"type":"turn.started"}`, `{"type":"turn.started"}{}`, 1),
		"unfinished item":          strings.Replace(validEvents, `"text":"Synthetic approved answer."`, `"text":"Synthetic approved answer.","status":"in_progress"`, 1),
		"only commentary":          strings.Replace(validEvents, `"text":"Synthetic approved answer."`, `"text":"Synthetic approved answer.","phase":"commentary"`, 1),
		"unknown phase":            strings.Replace(validEvents, `"text":"Synthetic approved answer."`, `"text":"Synthetic approved answer.","phase":"unknown"`, 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := parseEvents([]byte(data), "qwen3.5:4b")
			if err == nil || r.Complete || r.Text != "" {
				t.Fatalf("accepted uncertain output: %+v %v", r, err)
			}
		})
	}
}

func TestNativeEventBoundsAndUnicode(t *testing.T) {
	for _, data := range []string{strings.Repeat("x", maxOutputBytes+1), `{"type":"thread.started","thread_id":"` + strings.Repeat("x", maxLineBytes) + `"}`, `{"a":` + strings.Repeat("[", 34) + strings.Repeat("]", 34) + `}`} {
		if _, err := parseEvents([]byte(data), "qwen3.5:4b"); err == nil {
			t.Fatal("oversized or deeply nested events accepted")
		}
	}
	valid := strings.Replace(validEvents, "Synthetic approved answer.", `Unicode \ud83d\ude00 and literal \\ud800.`, 1)
	if _, err := parseEvents([]byte(valid), "qwen3.5:4b"); err != nil {
		t.Fatal(err)
	}
}
