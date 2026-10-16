package revision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/outcomes"
)

var ErrInvalid = errors.New("invalid checked revision request")

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

// Digest hashes the exact UTF-8 text bytes, without normalizing whitespace,
// Unicode, line endings, Markdown, or JSON escaping.
func Digest(text string) string { h := sha256.Sum256([]byte(text)); return hex.EncodeToString(h[:]) }

// Load rejects duplicate members, unknown/case-variant fields, invalid UTF-8,
// trailing data and nesting beyond 64. It validates limits before execution.
func Load(reader io.Reader) (Request, error) {
	var request Request
	data, err := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return request, invalid("request must be readable JSON of at most 1MiB")
	}
	if err := strictDecode(data, &request); err != nil {
		return Request{}, invalid("request must use strict JSON and exact known field names")
	}
	return request, Validate(request)
}

func Validate(request Request) error {
	if request.Schema != Schema {
		return invalid("schema must be 1")
	}
	if _, err := outcomes.Key(outcomes.Task{ID: request.Task, Revision: request.Revision}); err != nil {
		return invalid("task and revision must be nonempty bounded UTF-8 without controls or surrounding whitespace")
	}
	if !hash(request.Source.SHA256) {
		return invalid("source sha256 must be a lowercase SHA-256 digest")
	}
	if request.Source.Text != nil {
		if request.Source.Key != "" || request.Source.Version != 0 {
			return invalid("inline source cannot also reference an artifact")
		}
		if !validText(*request.Source.Text) {
			return invalid("source text must contain 1..262144 valid UTF-8 bytes")
		}
	} else if !resultKey(request.Source.Key) || request.Source.Version != 1 {
		return invalid("reference must pin version 1 of revision/v1/results/<lowercase SHA-256>")
	}
	if len(request.Edits) < 1 || len(request.Edits) > MaxEdits {
		return invalid("requires 1..32 explicit edits")
	}
	for _, edit := range request.Edits {
		if edit.Old == "" || edit.Old == edit.New || !utf8.ValidString(edit.Old) || !utf8.ValidString(edit.New) || len(edit.Old) > MaxReplacementBytes || len(edit.New) > MaxReplacementBytes || edit.ExpectedMatches < 1 || edit.ExpectedMatches > 4096 {
			return invalid("edits require changed valid UTF-8 literals of at most 16KiB each and 1..4096 expected matches")
		}
	}
	if err := evaluation.ValidateChecks(request.SourceChecks); err != nil {
		return invalid("source_checks must contain 1..64 valid deterministic checks")
	}
	if err := evaluation.ValidateChecks(request.Checks); err != nil {
		return invalid("checks must contain 1..64 valid deterministic checks")
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > MaxRequestBytes {
		return invalid("encoded request exceeds 1MiB")
	}
	return nil
}

func validText(text string) bool {
	return len(text) > 0 && len(text) <= MaxTextBytes && utf8.ValidString(text)
}
func hash(text string) bool {
	if len(text) != 64 {
		return false
	}
	for _, c := range text {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
func resultKey(key string) bool {
	return strings.HasPrefix(key, ResultPrefix) && hash(strings.TrimPrefix(key, ResultPrefix))
}
func requestDigest(request Request) string {
	request.TrackOutcome = false
	encoded, _ := json.Marshal(request)
	return Digest(string(encoded))
}

// RequestDigest returns the canonical operation identity. Accounting opt-in is
// excluded because it changes neither the transformation nor its predicates.
func RequestDigest(request Request) (string, error) {
	if err := Validate(request); err != nil {
		return "", err
	}
	return requestDigest(request), nil
}

// strictDecode supplements encoding/json's permissive case-insensitive field
// lookup. RawMessage comparison values may have arbitrary JSON field names.
func strictDecode(data []byte, target any) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	value, err := readJSON(d, 0)
	if err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	if err := exactFields(value, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		value := make(map[string]any)
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := value[key]; exists {
				return nil, ErrInvalid
			}
			value[key], err = readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return value, nil
	case '[':
		value := []any{}
		for d.More() {
			item, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return value, nil
	default:
		return nil, ErrInvalid
	}
}

func exactFields(value any, typ reflect.Type) error {
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return exactFields(value, typ.Elem())
	}
	if value == nil {
		return ErrInvalid
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		fields := make(map[string]reflect.Type)
		required := make(map[string]bool)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name != "" && name != "-" {
				fields[name] = field.Type
				required[name] = true
				for _, option := range tag[1:] {
					if option == "omitempty" {
						required[name] = false
					}
				}
			}
		}
		for name, mandatory := range required {
			if _, present := object[name]; mandatory && !present {
				return ErrInvalid
			}
		}
		for key, item := range object {
			field, found := fields[key]
			if !found {
				return ErrInvalid
			}
			if err := exactFields(item, field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return ErrInvalid
		}
		for _, item := range items {
			if err := exactFields(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
