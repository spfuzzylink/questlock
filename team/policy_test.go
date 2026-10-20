package team

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestReviewedLocalPolicyChangesIdentityWithoutChangingBaseline(t *testing.T) {
	r := fixture()
	before, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := MasterPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	task := TaskDigest(r)
	r.PolicyRevision = "local-json-memory-1"
	zero := float64(0)
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", Temperature: &zero}
	r.Master.MaxInputTokens = 8192
	r.Reviewers = append([]Role(nil), r.Reviewers...)
	for i := range r.Reviewers {
		r.Reviewers[i].Ollama = r.Master.Ollama
	}
	after, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	newPrompt, err := MasterPrompt(r)
	if err != nil || before == after || TaskDigest(r) != task || prompt != newPrompt {
		t.Fatal("policy must separate execution receipts while preserving exact baseline task and prompt", err)
	}
	w, err := workerRequest(r, r.Master, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Policy.Local.Spec.Ollama != r.Master.Ollama {
		t.Fatal("local options were not forwarded")
	}
	// Reviewer requests never contain the answer-check oracle or another vote.
	w, err = workerRequest(r, r.Reviewers[0], `{"answer":17}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Spec.Input, "expected") || strings.Contains(w.Spec.Instructions, `"answer":17`) {
		t.Fatal("expected answer leaked into reviewer instructions")
	}
}

func TestOmittedLocalPolicyPreservesPublishedTeamIdentity(t *testing.T) {
	// Captured from the published v0.9.0 binary for fixture(), before these
	// optional fields existed. Existing durable roles must keep their keys.
	fp, err := Fingerprint(fixture())
	if err != nil || fp != "4e640bb212643262eeaf94a98b97931a574e6053d924943e2e8a322719be8bfe" {
		t.Fatal("legacy team identity changed", fp, err)
	}
}

func TestPolicyAndOllamaValidationPrecedeAdmissionAndClaims(t *testing.T) {
	for _, change := range []func(*Request){
		func(r *Request) { r.PolicyRevision = "../escape" },
		func(r *Request) {
			r.Master.Driver = "codex-oss"
			r.Master.Ollama = &inference.OllamaOptions{Format: "json"}
		},
		func(r *Request) { r.Master.Ollama = &inference.OllamaOptions{Format: "answer-enum"} },
		func(r *Request) { n := math.NaN(); r.Reviewers[0].Ollama = &inference.OllamaOptions{Temperature: &n} },
	} {
		r := fixture()
		change(&r)
		c, _ := setup(t)
		e := Engine{Broker: c, WorkerID: "local-test", Factory: func(context.Context, Role) (delegation.Runner, error) {
			t.Fatal("invalid policy constructed a provider")
			return nil, nil
		}, Admit: func(context.Context, func(context.Context) error) error {
			t.Fatal("invalid policy admitted work")
			return nil
		}}
		if _, err := e.Run(context.Background(), r, "all"); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestJSONModePreservesStrictAnswerAndReviewChecks(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json"}
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, true, false)
	got, err := e.Run(context.Background(), r, "all")
	if err == nil || got.Status != "quality_failed" || got.Text != "" || calls.Load() != 1 {
		t.Fatal("JSON mode weakened master checks", got, err)
	}
	for _, raw := range []string{`{"approved":false,"issues":[]}`, `{"approved":"true","issues":[]}`, `{"approved":true,"issues":["defect"]}`, "```json\n{\"approved\":true,\"issues\":[]}\n```"} {
		w, err := workerRequest(r, r.Reviewers[0], `{"answer":17}`)
		if err != nil {
			t.Fatal(err)
		}
		checks := w.Checks
		if _, pass := evaluation.CheckOutput(raw, checks); pass {
			t.Fatal("invalid reviewer accepted", raw)
		}
	}
	for _, pair := range [][2]string{{`{"provider":"Ollama"}`, `{"provider":"ollama"}`}, {`{"cost":0}`, `{"cost":null}`}} {
		if _, pass := evaluation.CheckOutput(pair[0], []evaluation.Check{{ID: "strict", Kind: "json_equals", Expected: json.RawMessage(pair[1])}}); pass {
			t.Fatal("quality contract weakened")
		}
	}
}

func TestJSONCandidateRetainsTypesWithoutOracleOrTransportQuotes(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json"}
	for _, candidate := range []string{`{"value":null,"nested":{"a":2}}`, `[1,false,"text"]`, `"a JSON string"`, `false`, `0`, `null`, `{"A":1,"a":2}`} {
		w, err := workerRequest(r, r.Reviewers[0], candidate)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Candidate json.RawMessage `json:"candidate"`
		}
		if json.Unmarshal([]byte(w.Spec.Input), &got) != nil {
			t.Fatal("invalid reference")
		}
		// RawMessage is emitted as a value, so exact JSON types survive the envelope.
		if string(got.Candidate) != candidate {
			t.Fatal("candidate JSON was encoded as text", string(got.Candidate), candidate)
		}
	}
	for _, candidate := range []string{`not JSON`, `{"a":1,"a":2}`, "```json\n{}\n```", `{} {}`, `null,"other":true`, `null,"candidate":{}`} {
		if _, err := workerRequest(r, r.Reviewers[0], candidate); err == nil {
			t.Fatal("malformed candidate accepted", candidate)
		}
	}
	// Legacy text tasks preserve their published representation.
	r.Master.Ollama = nil
	w, err := workerRequest(r, r.Reviewers[0], `{"answer":17}`)
	var got struct {
		Candidate string `json:"candidate"`
	}
	if err != nil || json.Unmarshal([]byte(w.Spec.Input), &got) != nil || got.Candidate != `{"answer":17}` {
		t.Fatal("legacy candidate transport changed", err)
	}
}

type invalidJSONProvider struct{ fakeProvider }

func (p *invalidJSONProvider) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	result, err := p.fakeProvider.Generate(ctx, r)
	result.Text = "answer 17"
	return result, err
}

func TestMalformedJSONMasterFailsBeforeAnyReviewerClaim(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json"}
	r.Checks = []evaluation.Check{{ID: "team-json-output", Kind: "contains", Text: "17"}}
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, false, false)
	e.Factory = func(_ context.Context, role Role) (delegation.Runner, error) {
		if role.ID != "master" {
			t.Fatal("malformed master started a reviewer")
		}
		return inference.Runner{Broker: c, Provider: &invalidJSONProvider{fakeProvider{calls: calls, role: role.ID}}}, nil
	}
	out, err := e.Run(context.Background(), r, "all")
	if err == nil || out.Status != "quality_failed" || out.Text != "" || calls.Load() != 1 {
		t.Fatal("malformed master was accepted or reported uncertain", out, err)
	}
	fp, _ := Fingerprint(r)
	for _, role := range r.Reviewers {
		if _, err := c.Get(context.Background(), key(fp, role.ID)); !notFound(err) {
			t.Fatal("malformed master created reviewer claim", err)
		}
	}
	e.Factory = nil
	out, err = e.Run(context.Background(), r, "all")
	if err == nil || out.Status != "quality_failed" || calls.Load() != 1 {
		t.Fatal("malformed master was regenerated", out, err)
	}
}

func TestDeclaredMasterTypesChangeExecutionButNotTaskOrCanonicalPrompt(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", PromptMode: "bounded_text"}
	before, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := MasterPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	task := TaskDigest(r)
	r.Master.Ollama.OutputTypes = map[string]string{"answer": "integer"}
	after, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	afterPrompt, err := MasterPrompt(r)
	if err != nil || before == after || TaskDigest(r) != task || afterPrompt != prompt {
		t.Fatal("runtime contract changed baseline information or reused old execution identity", err)
	}
	w, err := workerRequest(r, r.Master, "")
	if err != nil || !reflect.DeepEqual(w.Policy.Local.Spec.Ollama.OutputTypes, r.Master.Ollama.OutputTypes) {
		t.Fatal("declared types were not forwarded to the local runtime", err)
	}
}

func TestReviewerReferenceIncludesDeclaredTypesWithoutExpectedValues(t *testing.T) {
	r := fixture()
	r.Instructions = "Return JSON with waiting_count. Subtract running from total."
	r.Input = "There are 9 jobs total and 3 running."
	r.Checks = []evaluation.Check{{ID: "secret-oracle", Kind: "json_equals", Expected: json.RawMessage(`{"waiting_count":6}`)}}
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"waiting_count": "integer"}}
	for _, reviewer := range r.Reviewers {
		w, err := workerRequest(r, reviewer, `{"waiting_count":5}`)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if json.Unmarshal([]byte(w.Spec.Input), &data) != nil || len(data) != 5 {
			t.Fatal("unexpected reviewer reference", w.Spec.Input)
		}
		for _, name := range []string{"instructions", "input", "context", "candidate", "output_types"} {
			if _, ok := data[name]; !ok {
				t.Fatal("missing approved reviewer reference", name)
			}
		}
		if string(data["output_types"]) != `{"waiting_count":"integer"}` || string(data["candidate"]) != `{"waiting_count":5}` || strings.Contains(w.Spec.Input, "secret-oracle") || strings.Contains(w.Spec.Input, `"waiting_count":6`) {
			t.Fatal("expected value leaked or declared contract changed", w.Spec.Input)
		}
		if w.Policy.Local.Spec.Ollama != nil || len(w.Checks) != 2 || w.Checks[0].Kind != "json_valid" || w.Checks[1].Kind != "json_equals" || string(w.Checks[1].Expected) != `{"approved":true,"issues":[]}` {
			t.Fatal("master contract replaced reviewer approval checks")
		}
	}
}

func TestOmittedTypesPreserveExactLegacyReviewerReference(t *testing.T) {
	r := fixture()
	w, err := workerRequest(r, r.Reviewers[0], `{"answer":17}`)
	want := `{"instructions":"Return JSON with answer 17.","input":"Answer 17.","context":null,"candidate":"{\"answer\":17}"}`
	if err != nil || w.Spec.Input != want {
		t.Fatal("legacy reviewer reference bytes changed", w.Spec.Input, err)
	}
	r.Master.Ollama = &inference.OllamaOptions{Format: "json"}
	w, err = workerRequest(r, r.Reviewers[0], `{"answer":17}`)
	want = `{"instructions":"Return JSON with answer 17.","input":"Answer 17.","context":null,"candidate":{"answer":17}}`
	if err != nil || w.Spec.Input != want {
		t.Fatal("legacy JSON reviewer reference bytes changed", w.Spec.Input, err)
	}
}

type noPolicyCallsBroker struct{ calls atomic.Int64 }

func (b *noPolicyCallsBroker) Get(context.Context, string) (protocol.Artifact, error) {
	b.calls.Add(1)
	return protocol.Artifact{}, errors.New("unexpected broker read")
}
func (b *noPolicyCallsBroker) Publish(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
	b.calls.Add(1)
	return protocol.PublishResult{}, errors.New("unexpected broker claim")
}

func TestReviewerOutputTypesAreRejectedBeforeBrokerAdmissionOrClaims(t *testing.T) {
	for _, index := range []int{0, 1} {
		for _, types := range []map[string]string{{}, {"approved": "boolean"}, {"waiting_count": "integer"}} {
			r := fixture()
			r.Reviewers[index].Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: types}
			broker := new(noPolicyCallsBroker)
			e := Engine{Broker: broker, WorkerID: "fixture", Factory: func(context.Context, Role) (delegation.Runner, error) {
				t.Fatal("reviewer contract constructed a provider")
				return nil, nil
			}, Admit: func(context.Context, func(context.Context) error) error {
				t.Fatal("reviewer contract requested capacity")
				return nil
			}}
			if _, err := e.Run(context.Background(), r, "all"); err == nil || !strings.Contains(err.Error(), "master-only") || broker.calls.Load() != 0 {
				t.Fatal("reviewer contract reached the broker or was accepted", index, types, broker.calls.Load(), err)
			}
		}
	}
}

func TestCorrectOutputTypesDoNotWeakenWrongNumericalAnswerCheck(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"answer": "integer"}}
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := engine(c, calls, true, false) // integer 18 still violates the original integer 17 check.
	result, err := e.Run(context.Background(), r, "all")
	if err == nil || result.Status != "quality_failed" || result.Text != "" || calls.Load() != 1 {
		t.Fatal("declared types accepted an incorrect integer", result, calls.Load(), err)
	}
	fp, _ := Fingerprint(r)
	for _, reviewer := range r.Reviewers {
		if _, err := c.Get(context.Background(), key(fp, reviewer.ID)); !notFound(err) {
			t.Fatal("wrong numerical answer claimed a reviewer", err)
		}
	}
}

func TestDeclaredFieldRolesChangeExecutionButNotTaskOrCanonicalPrompt(t *testing.T) {
	r := fixture()
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", PromptMode: "bounded_text", OutputTypes: map[string]string{"answer": "integer"}}
	before, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := MasterPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	task := TaskDigest(r)
	w, err := workerRequest(r, r.Reviewers[0], `{"answer":17}`)
	wantReference := `{"instructions":"Return JSON with answer 17.","input":"Answer 17.","context":null,"candidate":{"answer":17},"output_types":{"answer":"integer"}}`
	if err != nil || w.Spec.Input != wantReference {
		t.Fatal("omitted semantic roles changed the existing typed reference", w.Spec.Input, err)
	}
	r.Master.Ollama.FieldRoles = map[string]string{"answer": "quantity"}
	after, err := Fingerprint(r)
	if err != nil {
		t.Fatal(err)
	}
	afterPrompt, err := MasterPrompt(r)
	if err != nil || before == after || TaskDigest(r) != task || afterPrompt != prompt {
		t.Fatal("field roles changed canonical baseline information or reused old execution", err)
	}
	declared := runtimeFieldRoles(r)
	declared["answer"] = "identifier"
	if r.Master.Ollama.FieldRoles["answer"] != "quantity" {
		t.Fatal("runtime disclosure mutated the approved contract")
	}
	r.Master.Ollama.FieldRoles = nil
	reset, err := Fingerprint(r)
	if err != nil || reset != before {
		t.Fatal("nil field roles failed to preserve existing typed identity", reset, err)
	}
}

func TestReviewerReferenceAddsOnlyManualFieldRolesWithoutExpectedValues(t *testing.T) {
	r := fixture()
	r.Instructions = "Return JSON with waiting_count. Subtract running from total."
	r.Input = "There are 9 jobs total and 3 running."
	r.Checks = []evaluation.Check{{ID: "secret-oracle", Kind: "json_equals", Expected: json.RawMessage(`{"waiting_count":6}`)}}
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"waiting_count": "integer"}, FieldRoles: map[string]string{"waiting_count": "quantity"}}
	for _, reviewer := range r.Reviewers {
		w, err := workerRequest(r, reviewer, `{"waiting_count":5}`)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(w.Spec.Input), &fields) != nil || len(fields) != 6 || string(fields["field_roles"]) != `{"waiting_count":"quantity"}` || string(fields["output_types"]) != `{"waiting_count":"integer"}` || string(fields["candidate"]) != `{"waiting_count":5}` || strings.Contains(w.Spec.Input, "secret-oracle") || strings.Contains(w.Spec.Input, `"waiting_count":6`) {
			t.Fatal("reviewer semantics leaked an oracle or changed the candidate", w.Spec.Input)
		}
		if w.Policy.Local.Spec.Ollama != nil || len(w.Checks) != 2 || string(w.Checks[1].Expected) != `{"approved":true,"issues":[]}` {
			t.Fatal("manual master semantics replaced reviewer approval rules")
		}
	}
}

func TestReviewerFieldRolesAreRejectedBeforeBrokerAdmissionOrClaims(t *testing.T) {
	for _, index := range []int{0, 1} {
		for _, roles := range []map[string]string{{}, {"approved": "policy_action"}, {"answer": "quantity"}} {
			r := fixture()
			r.Reviewers[index].Ollama = &inference.OllamaOptions{Format: "json", FieldRoles: roles}
			broker := new(noPolicyCallsBroker)
			e := Engine{Broker: broker, WorkerID: "fixture", Factory: func(context.Context, Role) (delegation.Runner, error) {
				t.Fatal("reviewer field roles constructed a provider")
				return nil, nil
			}, Admit: func(context.Context, func(context.Context) error) error {
				t.Fatal("reviewer field roles requested capacity")
				return nil
			}}
			if _, err := e.Run(context.Background(), r, "all"); err == nil || !strings.Contains(err.Error(), "master-only") || broker.calls.Load() != 0 {
				t.Fatal("reviewer field roles reached the broker or were accepted", index, roles, broker.calls.Load(), err)
			}
		}
	}
}

type wrongPolicyActionProvider struct{ fakeProvider }

func (p *wrongPolicyActionProvider) Generate(ctx context.Context, r inference.Request) (inference.Response, error) {
	result, err := p.fakeProvider.Generate(ctx, r)
	result.Text = `{"max_workers":2,"excess_work":"3"}`
	return result, err
}

func TestWellTypedWrongPolicyActionFailsOriginalCheckBeforeAnyReviewer(t *testing.T) {
	r := fixture()
	r.Instructions = "Return JSON with max_workers and excess_work. Use safe capacity and preserve excess work."
	r.Input = "There are 3 CPUs and memory for 2 workers. Excess work must wait."
	r.Checks = []evaluation.Check{{ID: "original-policy", Kind: "json_equals", Expected: json.RawMessage(`{"max_workers":2,"excess_work":"wait"}`)}}
	r.Master.Ollama = &inference.OllamaOptions{Format: "json", OutputTypes: map[string]string{"max_workers": "integer", "excess_work": "string"}, FieldRoles: map[string]string{"max_workers": "quantity", "excess_work": "policy_action"}}
	c, _ := setup(t)
	calls := new(atomic.Int64)
	e := Engine{Broker: c, WorkerID: "fixture", Factory: func(_ context.Context, role Role) (delegation.Runner, error) {
		if role.ID != "master" {
			t.Fatal("wrong policy action started a reviewer")
		}
		return inference.Runner{Broker: c, Provider: &wrongPolicyActionProvider{fakeProvider{calls: calls, role: role.ID}}}, nil
	}}
	result, err := e.Run(context.Background(), r, "all")
	if err == nil || result.Status != "quality_failed" || result.Text != "" || calls.Load() != 1 {
		t.Fatal("manual semantics weakened the original answer check", result, calls.Load(), err)
	}
	fp, _ := Fingerprint(r)
	for _, reviewer := range r.Reviewers {
		if _, err := c.Get(context.Background(), key(fp, reviewer.ID)); !notFound(err) {
			t.Fatal("wrong semantic answer claimed a reviewer", err)
		}
	}
}
