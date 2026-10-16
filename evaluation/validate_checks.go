package evaluation

import (
	"errors"
	"strings"
	"unicode/utf8"
)

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
		default:
			return errors.New("unsupported deterministic evaluation check")
		}
	}
	return nil
}
