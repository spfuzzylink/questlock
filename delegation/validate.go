package delegation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

// Load reads at most 1MiB of request JSON. Authorization is deliberately not a
// JSON field: only the caller can enable paid escalation on Router.AllowPaid.
func Load(reader io.Reader) (Request, error) {
	var request Request
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !utf8.Valid(data) {
		return request, errors.New("delegation request must be readable UTF-8 JSON of at most 1MiB")
	}
	if err := uniqueJSON(data); err != nil {
		return request, errors.New("delegation request must be JSON without duplicate keys, excessive nesting, or trailing data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("delegation request has an unknown field or invalid field type")
	}
	return request, nil
}

// Validate resolves all tier plans and checks before any factory/provider call.
// A configured hosted tier is rejected unless this caller explicitly opts in,
// even when the local tier could produce a passing answer.
func Validate(request Request, allowPaid bool) error {
	if request.SemanticShadow != nil {
		if _, err := semantic.NormalizeConfig(*request.SemanticShadow); err != nil {
			return err
		}
	}
	base := request.Spec
	if base.Provider != "" || base.Model != "" || base.Stream || base.ReasoningEffort != "" || base.MaxContextBytes != 0 || base.MaxInputTokens != 0 || base.MaxOutputTokens != 0 || base.CacheMode != "" || base.ReuseForSeconds != 0 || base.Prices != nil || base.MaxCostUSD != 0 || base.DailyBudgetUSD != 0 || base.SGLang != nil || base.OpenRouter != nil || base.Ollama != nil {
		return errors.New("request spec supplies task, revision, instructions, input and context only; model/resource settings belong to each tier spec")
	}
	if !inference.SelfHosted(request.Policy.Local.Spec) {
		return errors.New("local-first policy requires an explicit Ollama or self-hosted SGLang tier")
	}
	if request.Policy.Escalation != nil && request.Policy.Escalation.ID == request.Policy.Local.ID {
		return errors.New("delegation tiers require distinct IDs")
	}
	if len(request.Checks) < 1 || len(request.Checks) > 64 {
		return errors.New("delegation requires 1..64 explicit deterministic checks")
	}
	for _, tier := range tiers(request.Policy) {
		if !validID(tier.ID) {
			return errors.New("delegation tier IDs must be nonempty identifiers of at most 64 ASCII letters, digits, dots, dashes or underscores")
		}
		if tier.Spec.Task != "" || tier.Spec.Revision != "" || tier.Spec.Instructions != "" || tier.Spec.Input != "" || len(tier.Spec.Context) != 0 {
			return errors.New("tier spec must omit task, revision, instructions, input and context")
		}
		if tier.Spec.ReuseForSeconds < 1 {
			return errors.New("delegation requires an explicit positive snapshot reuse TTL for every tier")
		}
		outputLimit := int64(4096)
		if tier.Spec.Provider == "ollama" && !tier.Spec.Stream && tier.Spec.Ollama != nil && tier.Spec.Ollama.Think != nil && *tier.Spec.Ollama.Think {
			outputLimit = 8192
		}
		if tier.Spec.MaxInputTokens > 32768 || tier.Spec.MaxOutputTokens > outputLimit || tier.Spec.MaxContextBytes > 131072 {
			return errors.New("delegation limits are at most 32768 input tokens, 4096 output tokens (8192 only for explicit nonstream Ollama thinking), and 131072 context bytes")
		}
		if !inference.SelfHosted(tier.Spec) {
			if !allowPaid {
				return errors.New("hosted delegation requires caller AllowPaid permission; no tier has run")
			}
			if tier.Spec.Prices == nil || tier.Spec.MaxCostUSD <= 0 || tier.Spec.DailyBudgetUSD <= 0 {
				return errors.New("hosted delegation requires explicit prices and positive per-call and daily USD budgets")
			}
		}
		// Validate the original human-supplied task identity before hashing it.
		planSpec := tier.Spec
		planSpec.Task, planSpec.Revision = base.Task, base.Revision
		planSpec.Instructions, planSpec.Input = base.Instructions, base.Input
		planSpec.Context = base.Context
		if _, err := inference.BuildPlan(planSpec); err != nil {
			return err
		}
		// Reuse the evaluation package's bounded check validation instead of
		// maintaining a divergent set of supported quality-check kinds.
		template := tier.Spec
		suite := evaluation.Suite{Schema: evaluation.Schema, ID: "delegation-checks", Revision: "validate", Cases: []evaluation.Case{{ID: "request", Complexity: "simple", Instructions: base.Instructions, Input: base.Input, Context: base.Context, Checks: request.Checks}}, Configurations: []evaluation.Configuration{{ID: tier.ID, Spec: template}}}
		if err := evaluation.Validate(suite); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > 1<<20 {
		return errors.New("delegation request exceeds 1MiB or contains unencodable values")
	}
	return nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.", c)) {
			return false
		}
	}
	return true
}

func tiers(policy Policy) []Tier {
	result := []Tier{policy.Local}
	if policy.Escalation != nil {
		result = append(result, *policy.Escalation)
	}
	return result
}

func requestDigest(request Request) string {
	request.TrackOutcome = false // Accounting opt-in does not change generated work.
	request.SemanticShadow = nil // Observing opportunities cannot change exact reuse or claims.
	encoded, _ := json.Marshal(request)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func tierSpec(request Request, tier Tier, digest string) inference.Spec {
	spec := tier.Spec
	if spec.Prices != nil {
		prices := *spec.Prices
		spec.Prices = &prices
	}
	identity := sha256.Sum256([]byte(digest + "\x00" + tier.ID))
	spec.Task = "delegation-" + hex.EncodeToString(identity[:16])
	spec.Revision = "delegation-" + digest[:32]
	spec.Instructions, spec.Input = request.Spec.Instructions, request.Spec.Input
	spec.Context = append([]inference.ContextItem(nil), request.Spec.Context...)
	return spec
}

// uniqueJSON rejects ambiguous duplicate configuration/authorization members.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var read func(int) error
	read = func(depth int) error {
		if depth > 64 {
			return errors.New("too deeply nested")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		keys := make(map[string]bool)
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate or invalid object key")
				}
				keys[name] = true
			}
			if err := read(depth + 1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := read(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
