package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// DecodeDataJSON preserves case-sensitive application data (for example a
// json_equals expectation) while requiring exact struct field names. Callers
// impose their byte bound. This does not relax the sync wire format.
func DecodeDataJSON(data []byte, value any) error {
	if !utf8.Valid(data) || !validSyncEscapes(data) {
		return errors.New("invalid JSON Unicode")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return errors.New("JSON object required")
	}
	if err = walkDataJSON(d, t, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	var raw any
	d = json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err = d.Decode(&raw); err != nil {
		return err
	}
	if err = exactDataFields(raw, reflect.TypeOf(value)); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func walkDataJSON(d *json.Decoder, t json.Token, depth int) error {
	if depth > 24 {
		return errors.New("JSON nesting exceeds limit")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	count := 0
	for d.More() {
		count++
		if count > 256 {
			return errors.New("JSON container exceeds limit")
		}
		if delim == '{' {
			k, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := k.(string)
			folded := strings.ToLower(name)
			if !ok || seen[folded] {
				return errors.New("duplicate or case-alias JSON key")
			}
			seen[folded] = true
		}
		next, err := d.Token()
		if err != nil {
			return err
		}
		if err = walkDataJSON(d, next, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errors.New("invalid JSON container")
	}
	return nil
}

func exactDataFields(v any, typ reflect.Type) error {
	if typ == nil {
		return errors.New("JSON destination required")
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) || typ.PkgPath() == "time" {
		return nil
	}
	if v == nil {
		return nil
	} // semantic required-field validation belongs to caller
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return errors.New("JSON object required for struct")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" {
				continue
			}
			n := strings.Split(f.Tag.Get("json"), ",")[0]
			if n == "-" {
				continue
			}
			if n == "" {
				n = f.Name
			}
			fields[n] = f.Type
		}
		for k, val := range obj {
			f, ok := fields[k]
			if !ok {
				return errors.New("unknown or noncanonical JSON field")
			}
			if err := exactDataFields(val, f); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if arr, ok := v.([]any); ok {
			for _, val := range arr {
				if err := exactDataFields(val, typ.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if obj, ok := v.(map[string]any); ok {
			for _, val := range obj {
				if err := exactDataFields(val, typ.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
