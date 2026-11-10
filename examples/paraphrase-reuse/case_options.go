package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
)

var measurementFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validMeasurementBudget(b inference.TokenBudget) bool {
	return b.InputTokens > 0 && b.InputTokens <= 1e12 && b.OutputTokens > 0 && b.OutputTokens <= 1e12 && b.TotalTokens > 0 && b.TotalTokens <= 1e12
}

func loadMeasurementCases(path string) ([]byte, caseSet, error) {
	data := casesJSON
	if path != "" {
		file, err := privateFile(path, false)
		if err != nil {
			return nil, caseSet{}, errors.New("cases require an absolute private regular file")
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return nil, caseSet{}, errors.New("cases exceed the 1 MiB bound")
		}
	}
	set, err := decodeMeasurementCases(data)
	return data, set, err
}

// A custom snapshot changes benchmark data, never the production reuse policy.
// Keep the same six-case protocol and full-object checks in both measured arms.
func decodeMeasurementCases(data []byte) (caseSet, error) {
	bad := errors.New("invalid six-case snapshot or full-object business checks")
	var set caseSet
	if len(data) > 1<<20 || strictRecord(data, &set) != nil || set.Schema != 1 || strings.TrimSpace(set.Revision) == "" || len(set.Cases) != 6 {
		return caseSet{}, bad
	}
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.OriginalQuestion) == "" || len(c.Paraphrases) != 2 || c.Paraphrases[0] == c.Paraphrases[1] || len(c.Checks) != 2 || c.Checks[0].Kind != "json_valid" || c.Checks[1].Kind != "json_equals" || c.Checks[1].Pointer != "" || evaluation.ValidateChecks(c.Checks) != nil || len(c.OutputTypes) == 0 || len(c.OutputTypes) > 32 {
			return caseSet{}, bad
		}
		for _, question := range c.Paraphrases {
			if strings.TrimSpace(question) == "" || question == c.OriginalQuestion {
				return caseSet{}, bad
			}
		}
		var expected map[string]json.RawMessage
		if json.Unmarshal(c.Checks[1].Expected, &expected) != nil || expected == nil || len(expected) != len(c.OutputTypes) {
			return caseSet{}, bad
		}
		for field, kind := range c.OutputTypes {
			value, exists := expected[field]
			if !exists || !measurementFieldName.MatchString(field) || !measurementExpectedType(value, kind) {
				return caseSet{}, bad
			}
		}
		seen[c.ID] = true
	}
	return set, nil
}

func measurementExpectedType(raw json.RawMessage, kind string) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	switch kind {
	case "string":
		var value string
		return json.Unmarshal(raw, &value) == nil
	case "boolean":
		var value bool
		return json.Unmarshal(raw, &value) == nil
	case "integer":
		var value int64
		return json.Unmarshal(raw, &value) == nil
	case "number":
		var value float64
		return json.Unmarshal(raw, &value) == nil
	default:
		return false
	}
}
