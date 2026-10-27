package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/spfuzzylink/questlock/protocol"
)

// ErrReuseUncertain identifies resolver work whose outcome or accounting is not
// confirmed. The target stays uncertain even when its own Generate never ran:
// changing a retrieved source must not admit another verification automatically.
var ErrReuseUncertain = errors.New("reuse verification outcome is uncertain; inspect its receipt before retrying")

// ReuseResolver is an explicitly authorized, bounded equivalence policy. Resolve
// runs only after this runner owns the target claim and before target generation.
// It must preserve uncertainty and accounting for any verification work it owns.
type ReuseResolver interface {
	Identity() string
	Resolve(context.Context, Spec) (*ReuseResolution, error)
}

// ReuseSource pins a completed original generation. Resolved aliases cannot be
// sources, and a current source read must still match every supplied binding.
type ReuseSource struct {
	Key                string `json:"key"`
	Version            int64  `json:"version"`
	SHA256             string `json:"sha256"`
	OutputSHA256       string `json:"output_sha256"`
	RequestFingerprint string `json:"request_fingerprint"`
}

// ReuseResolution retains historical source usage in Response. It is evidence
// for answer reuse, not evidence of target inference or measured target savings.
type ReuseResolution struct {
	PolicyID        string      `json:"policy_id"`
	Source          ReuseSource `json:"source"`
	Response        Response    `json:"response"`
	VerificationKey string      `json:"verification_key"`
}

func validReuseID(value string) bool { return validID(value, 256) }

func reuseDigest(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func validReuseDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validReuseWindow(created, expires time.Time, ttl int64, now time.Time) bool {
	return ttl > 0 && !created.IsZero() && !created.After(now) && expires.After(created) && now.Before(expires) && now.Before(created.Add(time.Duration(ttl)*time.Second))
}

func sameResponse(a, b Response) bool { return reflect.DeepEqual(a, b) }

func cloneResolution(value *ReuseResolution) *ReuseResolution {
	// Responses contain nested pointers. Copy through their existing JSON
	// contract so caller-owned resolver values cannot mutate returned evidence.
	encoded, _ := json.Marshal(value)
	var copied ReuseResolution
	_ = json.Unmarshal(encoded, &copied)
	return &copied
}

func (r Runner) validateResolution(ctx context.Context, s Spec, resolution *ReuseResolution) (jobRecord, error) {
	var source jobRecord
	if resolution == nil || !validReuseID(resolution.PolicyID) || !validReuseID(resolution.VerificationKey) {
		return source, errors.New("invalid reuse resolution metadata")
	}
	ref := resolution.Source
	if ref.Version < 1 || !validReuseDigest(ref.SHA256) || !validReuseDigest(ref.OutputSHA256) || !validReuseDigest(ref.RequestFingerprint) || !strings.HasPrefix(ref.Key, "inference/v1/jobs/") || !validReuseDigest(strings.TrimPrefix(ref.Key, "inference/v1/jobs/")) {
		return source, errors.New("invalid reuse source reference")
	}
	a, err := r.Broker.Get(ctx, ref.Key)
	if err != nil {
		return source, errors.New("reuse source unavailable; fresh generation is not authorized")
	}
	if a.Key != ref.Key || a.Version != ref.Version || reuseDigest(a.Content) != ref.SHA256 {
		return source, errors.New("reuse source version or hash changed")
	}
	// Strict raw decoding rejects duplicate/null envelopes without forbidding
	// harmless future job metadata. Full semantic bindings are checked below.
	var fields map[string]json.RawMessage
	if protocol.DecodeSyncJSON([]byte(a.Content), &fields) != nil || json.Unmarshal([]byte(a.Content), &source) != nil || source.Schema != 1 || source.State != "complete" || source.Resolution != nil || ref.Key != "inference/v1/jobs/"+source.Fingerprint || source.RequestFingerprint != ref.RequestFingerprint || reuseDigest(source.RequestFingerprint+"\x00"+r.Provider.Identity()) != source.Fingerprint || source.Response == nil || !source.Response.Complete || source.Response.Text == "" {
		return jobRecord{}, errors.New("reuse source is not an original completed generation")
	}
	if !validReuseWindow(source.CreatedAt, source.ExpiresAt, s.ReuseForSeconds, r.now()) || validateUsage(source.Response.Usage) != nil || source.Response.Usage.InputTokens < 1 || source.Response.Usage.InputTokens > s.MaxInputTokens || source.Response.Usage.OutputTokens > s.MaxOutputTokens || reuseDigest(source.Response.Text) != ref.OutputSHA256 || !sameResponse(*source.Response, resolution.Response) {
		return jobRecord{}, errors.New("reuse source is stale or its response evidence is invalid")
	}
	return source, nil
}

func resolvedResult(record jobRecord, spec Spec) Result {
	result := savedResult(record, spec, true)
	result.ReuseKind, result.Resolution = "semantic", cloneResolution(record.Resolution)
	return result
}

// Lookup reads an eligible exact original generation without publishing a
// claim, counting tokens, reserving budget or calling Generate. Resolved aliases
// are a miss because this method has no policy authorizing their resolution.
// Pending and uncertain jobs remain errors and never turn into fresh admission.
func (r Runner) Lookup(ctx context.Context, s Spec) (Result, bool, error) {
	plan, err := BuildPlan(s)
	if err != nil {
		return Result{}, false, err
	}
	if r.Broker == nil || r.Provider == nil || r.Provider.Identity() == "" {
		return Result{}, false, errors.New("broker and identified provider are required")
	}
	if guarded, ok := r.Provider.(interface{ RefreshIdentity(context.Context) error }); ok {
		if err := guarded.RefreshIdentity(ctx); err != nil {
			return Result{}, false, err
		}
	}
	fingerprint := reuseDigest(plan.Fingerprint + "\x00" + r.Provider.Identity())
	a, err := r.Broker.Get(ctx, "inference/v1/jobs/"+fingerprint)
	if brokerCode(err, "not_found") {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var source jobRecord
	var fields map[string]json.RawMessage
	if a.Key != "inference/v1/jobs/"+fingerprint || protocol.DecodeSyncJSON([]byte(a.Content), &fields) != nil || json.Unmarshal([]byte(a.Content), &source) != nil || source.Schema != 1 || source.Fingerprint != fingerprint || source.RequestFingerprint != "" && source.RequestFingerprint != plan.Fingerprint {
		return Result{}, false, errors.New("invalid stored inference record")
	}
	switch source.State {
	case "pending", "uncertain":
		return Result{Fingerprint: fingerprint}, false, ErrInFlight
	case "incomplete":
		return Result{Fingerprint: fingerprint}, false, ErrIncomplete
	case "limit_exceeded":
		if source.BudgetDiagnostic != nil && validBudgetDiagnostic(*source.BudgetDiagnostic) {
			return Result{Fingerprint: fingerprint}, false, &BudgetError{Diagnostic: *source.BudgetDiagnostic}
		}
		return Result{Fingerprint: fingerprint}, false, ErrBudgetExceeded
	case "blocked", "resolved":
		return Result{}, false, nil
	case "complete":
		if source.Response == nil || !source.Response.Complete || source.Response.Text == "" || validateUsage(source.Response.Usage) != nil || source.Response.Usage.InputTokens < 1 {
			return Result{}, false, errors.New("invalid stored completed response")
		}
		if source.Response.Usage.InputTokens > s.MaxInputTokens || source.Response.Usage.OutputTokens > s.MaxOutputTokens || !validReuseWindow(source.CreatedAt, source.ExpiresAt, s.ReuseForSeconds, r.now()) {
			return Result{}, false, nil
		}
		result := savedResult(source, s, true)
		now := time.Now().UTC()
		result.Measurement = &Measurement{StartedAt: now, EndedAt: now, Fingerprint: fingerprint, Outcome: "reused", PhaseMS: map[string]float64{}}
		return result, true, nil
	default:
		return Result{}, false, errors.New("unknown stored inference state")
	}
}
