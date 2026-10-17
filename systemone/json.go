package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/spfuzzylink/questlock/protocol"
)

// Load rejects ambiguous duplicate keys, unknown fields, trailing values, and
// oversized inputs before any native dependency or model is contacted.
func Load(reader io.Reader) (Request, error) {
	var request Request
	data, err := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return request, errors.New("cannot read bounded typed-choice request")
	}
	if strictJSON(data, &request) != nil {
		return request, errors.New("typed-choice request is invalid, ambiguous, or contains unsupported fields")
	}
	return request, request.Validate()
}

func strictJSON(data []byte, value any) error {
	// The shared protocol decoder rejects unpaired surrogate escapes before
	// encoding/json can silently replace them with U+FFFD.
	if err := protocol.DecodeSyncJSON(data, value); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON depth exceeds bound")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			// encoding/json also accepts case-insensitive struct field aliases.
			// This protocol accepts only canonical lowercase keys and labels.
			if !ok || !labelPattern.MatchString(name) || seen[name] {
				return errors.New("duplicate or noncanonical JSON key")
			}
			seen[name] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}
