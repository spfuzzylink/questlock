package evaluation

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// SupportedCheckKinds returns the deterministic kinds accepted by ValidateChecks
// in stable order. The returned slice is independent of subsequent calls.
func SupportedCheckKinds() []string {
	return []string{"json_valid", "json_equals", "contains", "not_contains", "word_count", "character_count", "paragraph_count"}
}

// ValidateChecks validates one bounded set of the deterministic predicates
// supported by CheckOutput, without constructing an inference configuration.
func ValidateChecks(checks []Check) error {
	if len(checks) < 1 || len(checks) > 64 {
		return errors.New("each evaluation case requires 1..64 deterministic checks")
	}
	checksSeen := make(map[string]bool)
	for _, check := range checks {
		if !validID(check.ID) || checksSeen[check.ID] {
			return errors.New("evaluation check IDs must be unique bounded identifiers within a case")
		}
		checksSeen[check.ID] = true
		if check.Kind != "word_count" && check.Kind != "character_count" && check.Kind != "paragraph_count" && (check.Min != nil || check.Max != nil) {
			return errors.New("count bounds are supported only by text count checks")
		}
		switch check.Kind {
		case "json_valid":
			if check.Pointer != "" || len(check.Expected) != 0 || check.Text != "" || check.IgnoreCase {
				return errors.New("json_valid checks do not accept comparison fields")
			}
		case "json_equals":
			if !validPointer(check.Pointer) || !utf8.ValidString(check.Pointer) || check.Text != "" || check.IgnoreCase {
				return errors.New("json_equals checks require a valid JSON pointer and expected JSON value")
			}
			if _, err := decodeJSON(check.Expected); err != nil {
				return errors.New("json_equals expected value must be bounded strict JSON")
			}
		case "contains", "not_contains":
			if strings.TrimSpace(check.Text) == "" || len(check.Text) > 4096 || !utf8.ValidString(check.Text) || check.Pointer != "" || len(check.Expected) != 0 {
				return errors.New("literal checks require nonempty bounded text and no JSON comparison fields")
			}
		case "word_count", "character_count", "paragraph_count":
			if check.Pointer != "" || len(check.Expected) != 0 || check.Text != "" || check.IgnoreCase {
				return errors.New("text count checks do not accept JSON or literal comparison fields")
			}
			if check.Min == nil && check.Max == nil || check.Min != nil && (*check.Min < 0 || *check.Min > MaxCountTextBytes) || check.Max != nil && (*check.Max < 0 || *check.Max > MaxCountTextBytes) || check.Min != nil && check.Max != nil && *check.Min > *check.Max {
				return errors.New("text count checks require inclusive min and/or max bounds in 0..4194304 with min <= max")
			}
		default:
			return errors.New("unsupported deterministic evaluation check")
		}
	}
	return nil
}
