package inference

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/spfuzzylink/questlock/protocol"
)

const maxOllamaOutputTypesSchemaBytes = 4096

const ollamaOutputTypesGuidance = "The type contract specifies representation only. Derive values from the approved task and supplied input. Preserve copied strings exactly, including numeric-looking codes. Use the task's stated action or policy when a field requests one; do not substitute an unrelated computed quantity. Compute numeric quantities only where requested. Recheck every requested field against its source before returning the object."

type ollamaOutputProperty struct {
	Type string `json:"type"`
}

type ollamaOutputSchema struct {
	Type                 string                          `json:"type"`
	Properties           map[string]ollamaOutputProperty `json:"properties"`
	Required             []string                        `json:"required"`
	AdditionalProperties bool                            `json:"additionalProperties"`
}

func ollamaOutputTypesSchema(options *OllamaOptions) (json.RawMessage, error) {
	if options == nil || options.OutputTypes == nil {
		return nil, nil
	}
	if options.Format != "json" || len(options.OutputTypes) < 1 || len(options.OutputTypes) > 32 {
		return nil, errors.New("ollama output_types requires format=json and 1..32 declared primitive fields")
	}
	schema := ollamaOutputSchema{Type: "object", Properties: make(map[string]ollamaOutputProperty, len(options.OutputTypes))}
	for name, typ := range options.OutputTypes {
		if !ollamaOutputFieldName(name) {
			return nil, errors.New("ollama output_types field names require lower ASCII snake_case identifiers of at most 64 bytes")
		}
		switch typ {
		case "integer", "string", "number", "boolean":
		default:
			return nil, errors.New("ollama output_types supports only integer, string, number, or boolean")
		}
		schema.Properties[name] = ollamaOutputProperty{Type: typ}
		schema.Required = append(schema.Required, name)
	}
	sort.Strings(schema.Required)
	encoded, err := json.Marshal(schema)
	if err != nil || len(encoded) > maxOllamaOutputTypesSchemaBytes {
		return nil, errors.New("ollama output_types schema exceeds 4096 bytes")
	}
	return encoded, nil
}

func ollamaOutputFieldName(name string) bool {
	if len(name) < 1 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '_' {
		return false
	}
	for i, c := range []byte(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c != '_' || i == 0 || name[i-1] == '_' {
			return false
		}
	}
	return true
}

type ollamaRenderedRequest struct {
	system, prompt string
	format         any
	schemaBytes    int
}

func renderOllamaRequest(req Request, installedSystem string) (ollamaRenderedRequest, error) {
	system, prompt := ollamaPrompt(req)
	rendered := ollamaRenderedRequest{system: system, prompt: prompt}
	if req.Ollama != nil && req.Ollama.Format != "" {
		rendered.format = req.Ollama.Format
	}
	schema, err := ollamaOutputTypesSchema(req.Ollama)
	if err != nil || schema == nil {
		return rendered, err
	}
	// This is declared decoder context, never a check expectation or answer.
	// Preserve the effective system: explicit system text already overrides
	// Ollama's default; an empty system would use the installed default.
	declared, _ := json.Marshal(req.Ollama.OutputTypes)
	if system == "" {
		system = installedSystem
	}
	parts := []string{}
	for _, part := range []string{system, "Output field types (JSON object):\n" + string(declared), ollamaOutputTypesGuidance} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	rendered.system = strings.Join(parts, "\n\n")
	rendered.format, rendered.schemaBytes = schema, len(schema)
	return rendered, nil
}

func (r ollamaRenderedRequest) inputBytes() int {
	return len(r.system) + len(r.prompt) + r.schemaBytes
}

func validOllamaOutputTypes(text string, types map[string]string) bool {
	if len(text) > maxProviderResponseBytes {
		return false
	}
	var object map[string]json.RawMessage
	if protocol.DecodeDataJSON([]byte(text), &object) != nil || len(object) != len(types) {
		return false
	}
	for name, typ := range types {
		raw, exists := object[name]
		if !exists {
			return false
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return false
		}
		numeric := raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9'
		switch typ {
		case "string":
			if raw[0] != '"' {
				return false
			}
		case "boolean":
			if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
				return false
			}
		case "number":
			if !numeric {
				return false
			}
		case "integer":
			if !numeric || !ollamaJSONInteger(raw) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// JSON Schema integer means an integral value, including 1.0 and 10e-1.
// Inspect decimal scale without float rounding or allocating a big integer or
// rational. Exponent magnitude saturates at the bounded input length.
func ollamaJSONInteger(number []byte) bool {
	mantissaEnd, fractionDigits := len(number), 0
	dot, exponent := -1, 0
	for i, c := range number {
		if c == '.' {
			dot = i
		}
		if c == 'e' || c == 'E' {
			mantissaEnd = i
			start, sign := i+1, 1
			if number[start] == '+' || number[start] == '-' {
				if number[start] == '-' {
					sign = -1
				}
				start++
			}
			limit := len(number) + 1
			for _, digit := range number[start:] {
				if exponent > limit/10 || exponent*10+int(digit-'0') > limit {
					exponent = limit
					break
				}
				exponent = exponent*10 + int(digit-'0')
			}
			exponent *= sign
			break
		}
	}
	if dot >= 0 {
		fractionDigits = mantissaEnd - dot - 1
	}
	trailingZeros, nonzero := 0, false
	for i := mantissaEnd - 1; i >= 0; i-- {
		c := number[i]
		if c >= '1' && c <= '9' {
			nonzero = true
			break
		}
		if c == '0' {
			trailingZeros++
		}
	}
	return !nonzero || exponent >= fractionDigits-trailingZeros
}
