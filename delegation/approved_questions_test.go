package delegation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/semantic"
)

func approvedQuestionFixture() ApprovedQuestionSet {
	return ApprovedQuestionSet{ReadOnly: true, Revision: "application-r1", Intent: "explain-approved-status", Bindings: map[string]string{"incident": "INC-17", "evidence": "notes-r1"}, CanonicalQuestion: "Explain the approved status of INC-17.", Forms: []string{"Summarize the approved status of INC-17.", "Explain the approved status of INC-17."}}
}

func TestApprovedQuestionsExactFormsShareOneOrdinaryRequest(t *testing.T) {
	approved := approvedQuestionFixture()
	request := testRequest()
	request.TrackOutcome = true
	request.Spec.Input = approved.Forms[0]
	request.Spec.Context = []inference.ContextItem{{ID: "approved-notes", Text: "Fictional evidence INC-17: required gate is pending.", Stable: true, Required: true}}
	beforeRequest, _ := json.Marshal(request)
	beforePolicy, _ := json.Marshal(approved)
	prepared, decision, err := ApplyApprovedQuestions(request, approved)
	if err != nil || !decision.Matched || len(decision.PolicySHA256) != 64 || prepared.Spec.Input != approved.CanonicalQuestion || prepared.Spec.Revision != "approved-questions-v1-"+decision.PolicySHA256 || len(prepared.Spec.Revision) > 128 {
		t.Fatalf("approved form was not prepared: %+v %+v %v", prepared, decision, err)
	}
	unchanged := prepared
	unchanged.Spec.Input, unchanged.Spec.Revision = request.Spec.Input, request.Spec.Revision
	if !reflect.DeepEqual(unchanged, request) {
		t.Fatal("helper changed fields beyond input and revision")
	}
	afterRequest, _ := json.Marshal(request)
	afterPolicy, _ := json.Marshal(approved)
	if string(beforeRequest) != string(afterRequest) || string(beforePolicy) != string(afterPolicy) {
		t.Fatal("helper mutated caller-owned request, forms or bindings")
	}
	canonical := request
	canonical.Spec.Input = approved.CanonicalQuestion
	canonicalPrepared, canonicalDecision, err := ApplyApprovedQuestions(canonical, approved)
	if err != nil || canonicalDecision != decision || !reflect.DeepEqual(canonicalPrepared, prepared) || requestDigest(canonicalPrepared) != requestDigest(prepared) {
		t.Fatalf("canonical member did not share the exact request identity: %+v %+v %v", canonicalPrepared, canonicalDecision, err)
	}
	reordered := approved
	reordered.Forms = []string{approved.Forms[1], approved.Forms[0]}
	reordered.Bindings = map[string]string{"evidence": "notes-r1", "incident": "INC-17"}
	if got, d, err := ApplyApprovedQuestions(request, reordered); err != nil || d != decision || !reflect.DeepEqual(got, prepared) {
		t.Fatalf("equivalent declaration order changed request identity: %+v %+v %v", got, d, err)
	}
	if again, d, err := ApplyApprovedQuestions(request, approved); err != nil || d != decision || !reflect.DeepEqual(again, prepared) {
		t.Fatal("preparing the original request is not deterministic")
	}
}

func TestApprovedQuestionsUnknownFormsRemainByteEquivalent(t *testing.T) {
	approved := approvedQuestionFixture()
	for _, input := range []string{
		"List completed steps only and exclude every pending step.",
		strings.ToLower(approved.Forms[0]), approved.Forms[0] + " ", " " + approved.Forms[0], approved.Forms[0] + "\n",
		"Explain the approved status of INC-18.", ".*", "Summarize the approved status of INC-17!",
		strings.Repeat("x", 8193), string([]byte{0xff}), "",
	} {
		t.Run(input, func(t *testing.T) {
			request := testRequest()
			request.Spec.Input = input
			before, _ := json.Marshal(request)
			got, decision, err := ApplyApprovedQuestions(request, approved)
			after, _ := json.Marshal(got)
			if err != nil || decision != (ApprovedQuestionDecision{}) || !reflect.DeepEqual(got, request) || string(before) != string(after) {
				t.Fatalf("unknown bytes were generalized into an approved form: %+v %v", decision, err)
			}
		})
	}
	unicodePolicy := ApprovedQuestionSet{ReadOnly: true, Revision: "r1", Intent: "literal-unicode", CanonicalQuestion: "Explain café status.", Forms: []string{"Explain café status.", "Describe café status."}}
	request := testRequest()
	request.Spec.Input = "Explain cafe\u0301 status."
	if got, decision, err := ApplyApprovedQuestions(request, unicodePolicy); err != nil || decision.Matched || !reflect.DeepEqual(got, request) {
		t.Fatalf("Unicode bytes were normalized: %+v %v", decision, err)
	}
	// Literal whitespace and regex metacharacters can themselves be approved;
	// they never instruct the helper to normalize or match other forms.
	literal := unicodePolicy
	literal.Forms = []string{literal.CanonicalQuestion, "  literal .* café?\n"}
	request.Spec.Input = literal.Forms[1]
	if got, decision, err := ApplyApprovedQuestions(request, literal); err != nil || !decision.Matched || got.Spec.Input != literal.CanonicalQuestion {
		t.Fatalf("exact literal bytes were not recognized: %+v %v", decision, err)
	}
}

func TestApprovedQuestionsUnknownInputLeavesRevisionValidationToExecution(t *testing.T) {
	approved := approvedQuestionFixture()
	for _, revision := range []string{"", strings.Repeat("x", 129), "invalid revision", string([]byte{0xff}), "approved-questions-v1-previous"} {
		request := testRequest()
		request.Spec.Input = "An unapproved request."
		request.Spec.Revision = revision
		got, decision, err := ApplyApprovedQuestions(request, approved)
		if err != nil || decision != (ApprovedQuestionDecision{}) || !reflect.DeepEqual(got, request) {
			t.Fatalf("unknown wording gained unrelated request validation or changes: %+v %v", decision, err)
		}
	}
}

func TestApprovedQuestionIdentityBindsEveryPolicyFieldAndOriginalRevision(t *testing.T) {
	request := testRequest()
	approved := approvedQuestionFixture()
	request.Spec.Input = approved.Forms[0]
	_, baseline, err := ApplyApprovedQuestions(request, approved)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ApprovedQuestionSet){
		"revision":      func(p *ApprovedQuestionSet) { p.Revision += "-next" },
		"intent":        func(p *ApprovedQuestionSet) { p.Intent += "-changed" },
		"binding value": func(p *ApprovedQuestionSet) { p.Bindings["evidence"] = "notes-r2" },
		"binding key": func(p *ApprovedQuestionSet) {
			p.Bindings["evidence_revision"] = p.Bindings["evidence"]
			delete(p.Bindings, "evidence")
		},
		"binding addition":       func(p *ApprovedQuestionSet) { p.Bindings["scope"] = "internal-preview" },
		"binding removal":        func(p *ApprovedQuestionSet) { delete(p.Bindings, "evidence") },
		"canonical question":     func(p *ApprovedQuestionSet) { p.CanonicalQuestion = p.Forms[0] },
		"approved form addition": func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, "Give the approved status of INC-17.") },
		"approved form replacement": func(p *ApprovedQuestionSet) {
			p.Forms[1] = "Give the approved status of INC-17."
			p.CanonicalQuestion = p.Forms[1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := approvedQuestionFixture()
			change(&policy)
			_, got, err := ApplyApprovedQuestions(request, policy)
			if err != nil || !got.Matched || got.PolicySHA256 == baseline.PolicySHA256 {
				t.Fatalf("changed approval policy retained its prior identity: %+v %v", got, err)
			}
		})
	}
	request.Spec.Revision += "-next"
	if _, got, err := ApplyApprovedQuestions(request, approved); err != nil || !got.Matched || got.PolicySHA256 == baseline.PolicySHA256 {
		t.Fatalf("original request revision was dropped: %+v %v", got, err)
	}
}

func TestApprovedQuestionsPreserveNormalContractFingerprintBoundaries(t *testing.T) {
	approved := approvedQuestionFixture()
	original := testRequest()
	original.Spec.Input = approved.Forms[0]
	original.Policy.Escalation = nil
	base, _, err := ApplyApprovedQuestions(original, approved)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Request){
		"task":         func(r *Request) { r.Spec.Task += "-changed" },
		"instructions": func(r *Request) { r.Spec.Instructions += " Include pending gates." },
		"context": func(r *Request) {
			r.Spec.Context = []inference.ContextItem{{ID: "evidence", Text: "Fictional changed notes", Stable: true, Required: true}}
		},
		"checks": func(r *Request) {
			r.Checks = []evaluation.Check{{ID: "other", Kind: "contains", Expected: json.RawMessage(`"pending"`)}}
		},
		"model":      func(r *Request) { r.Policy.Local.Spec.Model = "different:fixture" },
		"TTL":        func(r *Request) { r.Policy.Local.Spec.ReuseForSeconds++ },
		"input cap":  func(r *Request) { r.Policy.Local.Spec.MaxInputTokens++ },
		"output cap": func(r *Request) { r.Policy.Local.Spec.MaxOutputTokens++ },
		"daily budget": func(r *Request) {
			r.Policy.Local.Spec.DailyTokenBudget = &inference.TokenBudget{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := original
			change(&request)
			prepared, decision, err := ApplyApprovedQuestions(request, approved)
			if err != nil || !decision.Matched || requestDigest(prepared) == requestDigest(base) {
				t.Fatalf("canonicalization erased a normal request boundary: %+v %v", decision, err)
			}
		})
	}
}

func TestApprovedQuestionsRejectInvalidPoliciesWithoutEffects(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	for name, change := range map[string]func(*ApprovedQuestionSet){
		"not read only":     func(p *ApprovedQuestionSet) { p.ReadOnly = false },
		"empty revision":    func(p *ApprovedQuestionSet) { p.Revision = "" },
		"long revision":     func(p *ApprovedQuestionSet) { p.Revision = strings.Repeat("x", 129) },
		"invalid revision":  func(p *ApprovedQuestionSet) { p.Revision = invalidUTF8 },
		"spaced revision":   func(p *ApprovedQuestionSet) { p.Revision = " r1" },
		"empty intent":      func(p *ApprovedQuestionSet) { p.Intent = "" },
		"long intent":       func(p *ApprovedQuestionSet) { p.Intent = strings.Repeat("x", 129) },
		"invalid intent":    func(p *ApprovedQuestionSet) { p.Intent = invalidUTF8 },
		"control intent":    func(p *ApprovedQuestionSet) { p.Intent = "answer\x00status" },
		"missing canonical": func(p *ApprovedQuestionSet) { p.CanonicalQuestion = "Not in the approved forms." },
		"empty canonical":   func(p *ApprovedQuestionSet) { p.CanonicalQuestion = "" },
		"long canonical":    func(p *ApprovedQuestionSet) { p.CanonicalQuestion = strings.Repeat("x", 8193) },
		"invalid canonical": func(p *ApprovedQuestionSet) { p.CanonicalQuestion = invalidUTF8 },
		"one form":          func(p *ApprovedQuestionSet) { p.Forms = []string{p.CanonicalQuestion} },
		"too many forms": func(p *ApprovedQuestionSet) {
			for i := 0; i < 31; i++ {
				p.Forms = append(p.Forms, fmt.Sprintf("question %d", i))
			}
		},
		"duplicate forms": func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, p.Forms[0]) },
		"empty form":      func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, "") },
		"blank form":      func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, " \n\t") },
		"invalid form":    func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, invalidUTF8) },
		"long form":       func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, strings.Repeat("x", 8193)) },
		"total form bytes": func(p *ApprovedQuestionSet) {
			for _, ch := range []string{"a", "b", "c", "d", "e"} {
				p.Forms = append(p.Forms, strings.Repeat(ch, 8192))
			}
		},
		"encoded total": func(p *ApprovedQuestionSet) { p.Forms = append(p.Forms, strings.Repeat("<", 6000)) },
		"too many bindings": func(p *ApprovedQuestionSet) {
			p.Bindings = map[string]string{}
			for i := 0; i < 33; i++ {
				p.Bindings[fmt.Sprintf("key-%d", i)] = "value"
			}
		},
		"empty binding key":     func(p *ApprovedQuestionSet) { p.Bindings[""] = "value" },
		"long binding key":      func(p *ApprovedQuestionSet) { p.Bindings[strings.Repeat("x", 65)] = "value" },
		"invalid binding key":   func(p *ApprovedQuestionSet) { p.Bindings[invalidUTF8] = "value" },
		"empty binding value":   func(p *ApprovedQuestionSet) { p.Bindings["incident"] = "" },
		"long binding value":    func(p *ApprovedQuestionSet) { p.Bindings["incident"] = strings.Repeat("x", 257) },
		"invalid binding value": func(p *ApprovedQuestionSet) { p.Bindings["incident"] = invalidUTF8 },
	} {
		t.Run(name, func(t *testing.T) {
			policy := approvedQuestionFixture()
			change(&policy)
			request := testRequest()
			request.Spec.Input = policy.Forms[0]
			beforePolicy, _ := json.Marshal(policy)
			got, decision, err := ApplyApprovedQuestions(request, policy)
			afterPolicy, _ := json.Marshal(policy)
			if err == nil || decision != (ApprovedQuestionDecision{}) || !reflect.DeepEqual(got, request) || string(beforePolicy) != string(afterPolicy) {
				t.Fatalf("invalid policy changed caller data: %+v %v", decision, err)
			}
		})
	}
}

func TestApprovedQuestionsRejectMixedOrInvalidRequestAndPreparedRevision(t *testing.T) {
	approved := approvedQuestionFixture()
	for name, change := range map[string]func(*Request){
		"controlled reuse":          func(r *Request) { r.SemanticReuse = &semantic.ReuseConfig{} },
		"shadow":                    func(r *Request) { r.SemanticShadow = &semantic.Config{} },
		"empty original revision":   func(r *Request) { r.Spec.Revision = "" },
		"long original revision":    func(r *Request) { r.Spec.Revision = strings.Repeat("x", 129) },
		"invalid original revision": func(r *Request) { r.Spec.Revision = string([]byte{0xff}) },
		"spaced original revision":  func(r *Request) { r.Spec.Revision = "snapshot one" },
		"control original revision": func(r *Request) { r.Spec.Revision = "snapshot\x00one" },
	} {
		t.Run(name, func(t *testing.T) {
			request := testRequest()
			request.Spec.Input = approved.Forms[0]
			change(&request)
			got, decision, err := ApplyApprovedQuestions(request, approved)
			if err == nil || decision != (ApprovedQuestionDecision{}) || !reflect.DeepEqual(got, request) {
				t.Fatalf("invalid request was rewritten: %+v %v", decision, err)
			}
		})
	}
	request := testRequest()
	request.Spec.Input = approved.Forms[0]
	prepared, _, err := ApplyApprovedQuestions(request, approved)
	if err != nil {
		t.Fatal(err)
	}
	got, decision, err := ApplyApprovedQuestions(prepared, approved)
	if err == nil || decision != (ApprovedQuestionDecision{}) || !reflect.DeepEqual(got, prepared) {
		t.Fatalf("second preparation created a new identity around existing work: %+v %v", decision, err)
	}
}

func TestApprovedQuestionsAcceptBoundedUTF8AndDeclarationLimits(t *testing.T) {
	approved := ApprovedQuestionSet{ReadOnly: true, Revision: strings.Repeat("é", 64), Intent: strings.Repeat("ü", 64), CanonicalQuestion: strings.Repeat("c", 8192), Forms: []string{strings.Repeat("c", 8192)}, Bindings: map[string]string{}}
	for i := 1; i < 32; i++ {
		approved.Forms = append(approved.Forms, fmt.Sprintf("literal form %d", i))
	}
	for i := 0; i < 32; i++ {
		approved.Bindings[fmt.Sprintf("%062d-x", i)] = strings.Repeat("é", 128)
	}
	request := testRequest()
	request.Spec.Input = approved.CanonicalQuestion
	request.Spec.Revision = strings.Repeat("é", 64)
	if got, decision, err := ApplyApprovedQuestions(request, approved); err != nil || !decision.Matched || got.Spec.Input != approved.CanonicalQuestion || len(got.Spec.Revision) > 128 {
		t.Fatalf("valid maximum fields rejected: %+v %v", decision, err)
	}
}
