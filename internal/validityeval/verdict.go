package validityeval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/semantic"
)

const maxBatchVerdictBytes = 512

// parseBatchVerdict requires four independently reported Boolean decisions for
// each of the two canonical questions. A false factor means the answer must be
// withheld; this protocol cannot distinguish rejection from uncertainty, so it
// remains unknown. Nothing is exposed on a partial or malformed parse. Trusted
// usage, receipts and execution evidence come from the adapter, never this JSON.
func parseBatchVerdict(data []byte) ([]semantic.ValidityResult, error) {
	invalid := errors.New("batch validity verdict must contain exactly i0, f0, c0, s0, i1, f1, c1 and s1 boolean decisions")
	if len(data) == 0 || len(data) > maxBatchVerdictBytes || !utf8.Valid(data) {
		return nil, invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, invalid
	}
	decisions := make([]semantic.ValidityResult, 2)
	seen := make(map[string]bool, 8)
	for d.More() {
		keyToken, err := d.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] || len(seen) == 8 {
			return nil, invalid
		}
		value, err := d.Token()
		boolean, ok := value.(bool)
		if err != nil || !ok {
			return nil, invalid
		}
		switch key {
		case "i0":
			decisions[0].SameIntent = boolean
		case "f0":
			decisions[0].SameFacts = boolean
		case "c0":
			decisions[0].SameConstraints = boolean
		case "s0":
			decisions[0].AnswerSupported = boolean
		case "i1":
			decisions[1].SameIntent = boolean
		case "f1":
			decisions[1].SameFacts = boolean
		case "c1":
			decisions[1].SameConstraints = boolean
		case "s1":
			decisions[1].AnswerSupported = boolean
		default:
			return nil, invalid
		}
		seen[key] = true
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 8 {
		return nil, invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid
	}
	for i := range decisions {
		decision := &decisions[i]
		decision.Verdict = "unknown"
		if decision.SameIntent && decision.SameFacts && decision.SameConstraints && decision.AnswerSupported {
			decision.Verdict = "accept"
		}
	}
	return decisions, nil
}
