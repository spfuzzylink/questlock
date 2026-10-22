package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
)

// ErrInvalidSpec deliberately omits reader errors, field names and task content.
var ErrInvalidSpec = errors.New("execution_spec_invalid")

// LoadSpec reads one approved task snapshot, bounded by MaxSpecBytes. Field
// names are exact JSON names; unknown or duplicate keys, null values, trailing
// data, malformed UTF-8 and nesting beyond 32 levels are rejected. All errors
// are controlled and contain no task content. Loading performs no network call.
func LoadSpec(r io.Reader) (inference.Spec, error) {
	if nilInterface(r) {
		return inference.Spec{}, ErrInvalidSpec
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxSpecBytes+1))
	if err != nil || len(b) == 0 || len(b) > MaxSpecBytes || !utf8.Valid(b) {
		return inference.Spec{}, ErrInvalidSpec
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	value, err := specValue(d, 0)
	if err != nil {
		return inference.Spec{}, ErrInvalidSpec
	}
	if _, err = d.Token(); err != io.EOF {
		return inference.Spec{}, ErrInvalidSpec
	}
	if _, ok := value.(map[string]any); !ok || !exactSpecKeys(value, reflect.TypeFor[inference.Spec]()) {
		return inference.Spec{}, ErrInvalidSpec
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var spec inference.Spec
	if d.Decode(&spec) != nil {
		return inference.Spec{}, ErrInvalidSpec
	}
	if _, err := inference.BuildPlan(spec); err != nil {
		return inference.Spec{}, ErrInvalidSpec
	}
	return spec, nil
}

func specValue(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, ErrInvalidSpec
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return nil, ErrInvalidSpec
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, ErrInvalidSpec
			}
			if _, exists := object[name]; exists {
				return nil, ErrInvalidSpec
			}
			value, err := specValue(d, depth+1)
			if err != nil {
				return nil, ErrInvalidSpec
			}
			object[name] = value
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, ErrInvalidSpec
		}
		return object, nil
	case '[':
		array := []any{}
		for d.More() {
			value, err := specValue(d, depth+1)
			if err != nil {
				return nil, ErrInvalidSpec
			}
			array = append(array, value)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, ErrInvalidSpec
		}
		return array, nil
	default:
		return nil, ErrInvalidSpec
	}
}

// encoding/json matches struct fields without case sensitivity. Require the
// declared JSON names too, so "task" and "Task" cannot overwrite one another.
func exactSpecKeys(value any, shape reflect.Type) bool {
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch shape.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := range shape.NumField() {
			field := shape.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for name, v := range object {
			field, known := fields[name]
			if !known || !exactSpecKeys(v, field) {
				return false
			}
		}
	case reflect.Slice:
		array, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range array {
			if !exactSpecKeys(v, shape.Elem()) {
				return false
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, v := range object {
			if !exactSpecKeys(v, shape.Elem()) {
				return false
			}
		}
	}
	return true
}
