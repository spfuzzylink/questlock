package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/inference"
)

// Load parses at most 4MiB of strict suite JSON. Unknown fields and duplicate
// object keys are rejected so misspelled limits cannot silently disappear.
func Load(reader io.Reader) (Suite, error) {
	var suite Suite
	data, err := io.ReadAll(io.LimitReader(reader, (4<<20)+1))
	if err != nil {
		return suite, errors.New("cannot read evaluation suite")
	}
	if _, err := decodeJSON(data); err != nil {
		return suite, errors.New("evaluation suite must be bounded JSON without duplicate keys or trailing data")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&suite); err != nil {
		return suite, errors.New("evaluation suite contains an unknown field or invalid field type")
	}
	return suite, Validate(suite)
}

// Validate checks every case/configuration plan before any runner is created.
func Validate(s Suite) error {
	if s.Schema != Schema || !validID(s.ID) || !validID(s.Revision) {
		return errors.New("evaluation suite requires schema 1 and nonempty bounded IDs")
	}
	if s.Objective != "" && s.Objective != "latency" && s.Objective != "generated_tokens" && s.Objective != "priced_cost" {
		return errors.New("evaluation objective must be latency, generated_tokens, or priced_cost")
	}
	if len(s.Cases) < 1 || len(s.Cases) > 30 || len(s.Configurations) < 1 || len(s.Configurations) > 10 {
		return errors.New("evaluation requires 1..30 cases and 1..10 configurations")
	}
	seen := make(map[string]bool)
	for _, config := range s.Configurations {
		if !validID(config.ID) || seen[config.ID] {
			return errors.New("evaluation configuration IDs must be unique bounded identifiers")
		}
		seen[config.ID] = true
		template := config.Spec
		if template.Task != "" || template.Revision != "" || template.Instructions != "" || template.Input != "" || len(template.Context) != 0 {
			return errors.New("configuration spec must leave task, revision, instructions, input, and context to its suite case")
		}
		if template.ReuseForSeconds < 1 {
			return errors.New("evaluation configurations require positive snapshot reuse TTL")
		}
		if config.MaxFirstRunMS < 0 || config.MaxFirstRunMS > 3600000 || config.MaxReuseMS < 0 || config.MaxReuseMS > 3600000 {
			return errors.New("evaluation latency limits must be 0..3600000 milliseconds")
		}
		if s.Objective == "priced_cost" && template.Prices == nil {
			return errors.New("priced_cost objective requires explicit prices for every configuration")
		}
	}
	digest := suiteDigest(s)
	seen = make(map[string]bool)
	for _, c := range s.Cases {
		if !validID(c.ID) || seen[c.ID] || (c.Complexity != "simple" && c.Complexity != "complex") {
			return errors.New("evaluation cases require unique bounded IDs and simple or complex classification")
		}
		seen[c.ID] = true
		if len(c.Checks) < 1 || len(c.Checks) > 64 {
			return errors.New("each evaluation case requires 1..64 deterministic checks")
		}
		checksSeen := make(map[string]bool)
		for _, check := range c.Checks {
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
				if !validPointer(check.Pointer) || check.Text != "" || check.IgnoreCase {
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
		for _, config := range s.Configurations {
			if _, err := inference.BuildPlan(caseSpec(s, c, config, digest)); err != nil {
				return fmt.Errorf("invalid evaluation plan for %s/%s: %w", c.ID, config.ID, err)
			}
		}
		for configID, fixture := range c.Fixtures {
			if !configurationExists(s, configID) || fixture.FirstRunMS < 0 || fixture.FirstRunMS > 3600000 || fixture.ReuseMS < 0 || fixture.ReuseMS > 3600000 || len(fixture.Response.Text) > 512<<10 ||
				fixture.Response.ID == "" || fixture.Response.Model == "" || !utf8.ValidString(fixture.Response.Text) || !validGenerationUsage(fixture.Response.Usage) {
				return errors.New("evaluation fixture has an unknown configuration or unsupported bounds")
			}
			for _, config := range s.Configurations {
				if config.ID == configID && fixture.Response.Model != config.Spec.Model {
					return errors.New("evaluation fixture model must match its explicit configuration")
				}
			}
		}
	}
	data, err := json.Marshal(s)
	if err != nil || len(data) > 4<<20 {
		return errors.New("evaluation suite exceeds 4MiB or contains unencodable values")
	}
	return nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func configurationExists(s Suite, id string) bool {
	for _, c := range s.Configurations {
		if c.ID == id {
			return true
		}
	}
	return false
}

func suiteDigest(s Suite) string {
	encoded, _ := json.Marshal(s)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func caseSpec(s Suite, c Case, config Configuration, digest string) inference.Spec {
	spec := config.Spec
	identity := sha256.Sum256([]byte(s.ID + "\x00" + s.Revision + "\x00" + c.ID + "\x00" + config.ID + "\x00" + digest))
	spec.Task = "evaluation-" + hex.EncodeToString(identity[:16])
	spec.Revision = "evaluation-" + s.Revision + "-" + digest[:16]
	spec.Instructions, spec.Input = c.Instructions, c.Input
	spec.Context = append([]inference.ContextItem(nil), c.Context...)
	return spec
}
