package team

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
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
