package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CheckOutput checks a response without retaining the response in the report.
// JSON checks reject duplicate keys, trailing data, and excessive nesting.
func CheckOutput(text string, checks []Check) ([]CheckResult, bool) {
	results := make([]CheckResult, 0, len(checks))
	passed := len(checks) > 0
	var document any
	var parsed bool
	var parseErr error
	for _, c := range checks {
		ok := false
		switch c.Kind {
		case "json_valid", "json_equals":
			if !parsed {
				document, parseErr = decodeJSON([]byte(text))
				parsed = true
			}
			if parseErr == nil {
				ok = c.Kind == "json_valid"
				if c.Kind == "json_equals" {
					actual, found := resolvePointer(document, c.Pointer)
					expected, err := decodeJSON(c.Expected)
					ok = found && err == nil && equalJSON(actual, expected)
				}
			}
		case "contains", "not_contains":
			haystack, needle := text, c.Text
			if c.IgnoreCase {
				haystack, needle = strings.ToLower(haystack), strings.ToLower(needle)
			}
			ok = strings.Contains(haystack, needle) && needle != ""
			if c.Kind == "not_contains" {
				ok = !strings.Contains(haystack, needle) && needle != ""
			}
		}
		results = append(results, CheckResult{ID: c.ID, Kind: c.Kind, Passed: ok})
		passed = passed && ok
	}
	return results, passed
}

func decodeJSON(data []byte) (any, error) {
	if len(data) > 4<<20 || !utf8.Valid(data) {
		return nil, errors.New("JSON must be valid UTF-8 and at most 4MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return v, nil
}

func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting exceeds 64")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := make(map[string]any)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid JSON object key")
				}
				if _, duplicate := m[k]; duplicate {
					return nil, errors.New("duplicate JSON object key")
				}
				v, err := readJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if t, err = d.Token(); err != nil || t != json.Delim('}') {
				return nil, errors.New("unclosed JSON object")
			}
			return m, nil
		case '[':
			a := make([]any, 0)
			for d.More() {
				v, err := readJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if t, err = d.Token(); err != nil || t != json.Delim(']') {
				return nil, errors.New("unclosed JSON array")
			}
			return a, nil
		}
		return nil, errors.New("unexpected JSON delimiter")
	}
	if n, ok := t.(json.Number); ok {
		if len(n.String()) > 256 {
			return nil, errors.New("JSON number exceeds supported size")
		}
		// Bound exponents before converting numbers to exact rationals.
		if i := strings.IndexAny(n.String(), "eE"); i >= 0 {
			exp, err := strconv.ParseInt(n.String()[i+1:], 10, 32)
			if err != nil || exp < -1000 || exp > 1000 {
				return nil, errors.New("JSON exponent exceeds supported range")
			}
		}
	}
	return t, nil
}

func equalJSON(a, b any) bool {
	if x, ok := a.(json.Number); ok {
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		xr, xok := new(big.Rat).SetString(x.String())
		yr, yok := new(big.Rat).SetString(y.String())
		return xok && yok && xr.Cmp(yr) == 0
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for key, value := range x {
			other, found := y[key]
			if !found || !equalJSON(value, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func resolvePointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !validPointer(pointer) {
		return nil, false
	}
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch value := document.(type) {
		case map[string]any:
			var found bool
			document, found = value[part]
			if !found {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(value) || strconv.Itoa(i) != part {
				return nil, false
			}
			document = value[i]
		default:
			return nil, false
		}
	}
	return document, true
}

func validPointer(pointer string) bool {
	if len(pointer) > 1024 || (pointer != "" && !strings.HasPrefix(pointer, "/")) {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
				return false
			}
		}
	}
	return true
}
