package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const approvedQuestionRevisionPrefix = "approved-questions-v1-"

// ApprovedQuestionSet is a trusted application's assertion that its exact Forms
// ask the same read-only question. Questlock does not infer that equivalence.
// CanonicalQuestion must be one of the 2..32 unique Forms. Questions are matched
// byte for byte, without trimming, case folding, Unicode normalization or regex.
// Bindings scope the request identity. Supply every fact the model needs through
// the canonical question, instructions and context; Bindings adds no prompt text.
//
// Revision and Intent are nonempty labels of at most 128 UTF-8 bytes. Bindings
// has at most 32 entries, with nonempty labels of at most 64 bytes for keys and
// 256 bytes for values. Each question is at most 8192 UTF-8 bytes. The complete
// encoded policy and original request revision must fit within 32KiB.
// These are caller approvals, never a list learned from model decisions.
type ApprovedQuestionSet struct {
	ReadOnly          bool              `json:"read_only"`
	Revision          string            `json:"revision"`
	Intent            string            `json:"intent"`
	Bindings          map[string]string `json:"bindings,omitempty"`
	CanonicalQuestion string            `json:"canonical_question"`
	Forms             []string          `json:"forms"`
}

// ApprovedQuestionDecision reports an exact membership match. PolicySHA256 binds
// the versioned, sorted policy and the original request revision; it is also the
// suffix of the prepared request revision. This is no model validity verdict.
type ApprovedQuestionDecision struct {
	Matched      bool   `json:"matched"`
	PolicySHA256 string `json:"policy_sha256,omitempty"`
}

// ApplyApprovedQuestions prepares an ordinary request for exact reuse. A known
// form changes only Spec.Input and Spec.Revision; all context, checks, limits,
// TTL, tiers and accounting settings retain the normal delegation behavior.
// The caller must approve the set independently of request/model data.
//
// This pure helper performs no I/O, provider work, lookup or publication. An
// unknown form returns the original request and a zero decision; its ordinary
// request validation is unchanged. An invalid policy or matched revision also
// returns the original request and a zero decision, with an error. Semantic
// reuse and shadow policies cannot be combined with this
// preparation. Apply once to an original request; execute or retry the prepared
// request directly. Preparing an already prepared revision is rejected so it
// cannot create a new identity around a pending or uncertain result.
func ApplyApprovedQuestions(request Request, approved ApprovedQuestionSet) (Request, ApprovedQuestionDecision, error) {
	invalid := errors.New("approved questions require an explicit bounded read-only policy and an original request without semantic reuse or shadow")
	if request.SemanticReuse != nil || request.SemanticShadow != nil || !approved.ReadOnly || !approvedQuestionLabel(approved.Revision, 128) || !approvedQuestionLabel(approved.Intent, 128) {
		return request, ApprovedQuestionDecision{}, invalid
	}
	if !approvedQuestionText(approved.CanonicalQuestion) || len(approved.Forms) < 2 || len(approved.Forms) > 32 || len(approved.Bindings) > 32 {
		return request, ApprovedQuestionDecision{}, invalid
	}
	for key, value := range approved.Bindings {
		if !approvedQuestionLabel(key, 64) || !approvedQuestionLabel(value, 256) {
			return request, ApprovedQuestionDecision{}, invalid
		}
	}
	forms := append([]string(nil), approved.Forms...)
	sort.Strings(forms)
	matched, canonicalFound := false, false
	total := 0
	for i, form := range forms {
		total += len(form)
		if !approvedQuestionText(form) || total > 32<<10 || i > 0 && form == forms[i-1] {
			return request, ApprovedQuestionDecision{}, invalid
		}
		matched = matched || form == request.Spec.Input
		canonicalFound = canonicalFound || form == approved.CanonicalQuestion
	}
	if !canonicalFound {
		return request, ApprovedQuestionDecision{}, invalid
	}
	approved.Forms = forms
	policyJSON, err := json.Marshal(approved)
	if err != nil || len(policyJSON) > 32<<10 {
		return request, ApprovedQuestionDecision{}, invalid
	}
	if !matched {
		return request, ApprovedQuestionDecision{}, nil
	}
	if !approvedRequestRevision(request.Spec.Revision) || strings.HasPrefix(request.Spec.Revision, approvedQuestionRevisionPrefix) {
		return request, ApprovedQuestionDecision{}, invalid
	}
	encoded, err := json.Marshal(struct {
		Schema           int             `json:"schema"`
		Policy           json.RawMessage `json:"policy"`
		OriginalRevision string          `json:"original_revision"`
	}{1, policyJSON, request.Spec.Revision})
	if err != nil || len(encoded) > 32<<10 {
		return request, ApprovedQuestionDecision{}, invalid
	}
	digest := sha256.Sum256(encoded)
	policySHA256 := hex.EncodeToString(digest[:])
	request.Spec.Input = approved.CanonicalQuestion
	request.Spec.Revision = approvedQuestionRevisionPrefix + policySHA256
	return request, ApprovedQuestionDecision{Matched: true, PolicySHA256: policySHA256}, nil
}

func approvedQuestionText(value string) bool {
	return len(value) > 0 && len(value) <= 8192 && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func approvedQuestionLabel(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func approvedRequestRevision(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}
