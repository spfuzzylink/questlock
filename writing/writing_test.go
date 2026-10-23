package writing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

var testContext = context.Background()

func realBroker(t *testing.T) *client.Client {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	token, err := db.CreateToken(testContext, "writing-fixture", "writing-tests")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(db))
	t.Cleanup(server.Close)
	broker, err := client.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func referenceBytes(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(Reference{Schema: 1, ID: "sample-profile", Revision: "profile-1", Facts: []string{"Maintains synthetic infrastructure fixtures."}, Style: []string{"Use concise supported statements."}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func evidenceBytes(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(Evidence{Schema: 1, ID: "sample-role", Revision: "evidence-1", Question: "What interests you about this company?", Facts: []string{"The sample company builds infrastructure tools."}, Sources: []string{"https://example.com/sample-role"}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixturePlan(t *testing.T) Plan {
	t.Helper()
	plan, err := Build(referenceBytes(t), evidenceBytes(t), DefaultRecipe("synthetic-local"))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type fixtureProvider struct {
	text                string
	counts, generations int
}

func (p *fixtureProvider) Identity() string { return "writing-deterministic-fixture" }
func (p *fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts++
	return 100, nil
}
func (p *fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.generations++
	return inference.Response{ID: "writing-fixture", Model: "synthetic-local", Complete: true, Text: p.text,
		Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 180}}, nil
}

func fixtureRouter(broker inference.Broker, provider *fixtureProvider) delegation.Router {
	return delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) {
		return inference.Runner{Broker: broker, Provider: provider, Now: func() time.Time { return time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC) }}, nil
	}}
}

func passingText() string {
	return strings.Repeat("clear ", 70) + "\n\n" + strings.Repeat("supported ", 70)
}

func TestBuildPreservesConstraintsAndChangesIdentityWithEvidence(t *testing.T) {
	plan := fixturePlan(t)
	again := fixturePlan(t)
	if !reflect.DeepEqual(plan, again) || ValidatePlan(plan) != nil || plan.Request.Policy.Escalation != nil || plan.Request.Policy.Local.Spec.Provider != "ollama" || len(plan.Request.Spec.Context) != 2 {
		t.Fatalf("writing snapshot not deterministic/local: %+v", plan)
	}
	for _, item := range plan.Request.Spec.Context {
		if !item.Required {
			t.Fatal("reference or current evidence may silently be dropped")
		}
	}
	if !strings.Contains(plan.Request.Spec.Instructions, "130-165 words in exactly 2 paragraphs") || len(plan.Request.Checks) != 4 || plan.Request.Checks[0].Kind != "word_count" || *plan.Request.Checks[0].Max != 165 || plan.Request.Checks[1].Kind != "paragraph_count" {
		t.Fatal("natural-language writing constraints were not carried into deterministic checks")
	}
	for _, change := range []string{"evidence facts", "evidence source", "reference style", "recipe model"} {
		reference, evidence, recipe := referenceBytes(t), evidenceBytes(t), DefaultRecipe("synthetic-local")
		switch change {
		case "evidence facts":
			evidence = []byte(strings.Replace(string(evidence), "infrastructure tools", "different tools", 1))
		case "evidence source":
			evidence = []byte(strings.Replace(string(evidence), "https://example.com/sample-role", "https://example.com/updated-role", 1))
		case "reference style":
			reference = []byte(strings.Replace(string(reference), "concise", "plain", 1))
		case "recipe model":
			recipe.Model = "another-local-model"
		}
		changed, err := Build(reference, evidence, recipe)
		if err != nil || changed.RequestSHA256 == plan.RequestSHA256 || changed.Request.Spec.Revision == plan.Request.Spec.Revision {
			t.Fatalf("%s silently preserved snapshot: %+v %v", change, changed, err)
		}
	}
	for _, change := range []func(*Plan){
		func(p *Plan) { p.ReferenceRevision = "unapproved-version" },
		func(p *Plan) { p.EvidenceSHA256 = Hash([]byte("different evidence")) },
		func(p *Plan) { p.Request.Spec.Instructions += " Invent an experience." },
		func(p *Plan) { p.Request.Checks[0].Max = nil },
	} {
		tampered := fixturePlan(t)
		change(&tampered)
		if ValidatePlan(tampered) == nil {
			t.Fatal("tampered plan passed approval binding")
		}
	}
}

func TestWritingDocumentsRejectAmbiguousUnknownAndOversizedFields(t *testing.T) {
	reference, evidence := string(referenceBytes(t)), string(evidenceBytes(t))
	for _, text := range []string{
		strings.Replace(reference, `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(reference, `"schema":1`, `"schema":1,"Schema":1`, 1),
		strings.Replace(reference, `"schema":1`, `"schema":1,"accepted":true`, 1),
		reference + `{}`,
		strings.Repeat(" ", MaxReferenceBytes) + reference,
		string([]byte{0xff}),
	} {
		if _, err := DecodeReference([]byte(text)); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	for _, text := range []string{
		strings.Replace(evidence, `"schema":1`, `"schema":1,"accepted":true`, 1),
		strings.Replace(evidence, `"revision":"evidence-1"`, `"revision":""`, 1),
		strings.Replace(evidence, `"question":`, `"Question":`, 1),
		strings.Repeat(" ", MaxEvidenceBytes) + evidence,
	} {
		if _, err := DecodeEvidence([]byte(text)); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	for _, change := range []func(*Recipe){
		func(r *Recipe) { r.MaxWords = 0 },
		func(r *Recipe) { r.MinWords = r.MaxWords + 1 },
		func(r *Recipe) { r.Paragraphs = 0 },
		func(r *Recipe) { r.ReuseForSeconds = 86401 },
		func(r *Recipe) { r.Forbidden = []string{" "} },
	} {
		recipe := DefaultRecipe("synthetic-local")
		change(&recipe)
		if _, err := Build([]byte(reference), []byte(evidence), recipe); err == nil {
			t.Fatal("invalid writing recipe accepted")
		}
	}
}

func TestExactRepeatCallsNoModelAndReviewIndexPreservesMetadata(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	provider := &fixtureProvider{text: passingText()}
	router := fixtureRouter(broker, provider)
	first, err := router.Run(testContext, plan.Request)
	if err != nil || first.Status != "completed" {
		t.Fatalf("fixture answer: %+v %v", first, err)
	}
	second, err := router.Run(testContext, plan.Request)
	if err != nil || provider.counts != 1 || provider.generations != 1 || !second.Attempts[0].Result.Reused || second.Attempts[0].Result.Measurement.GenerationCalled || !second.Attempts[0].QualityPassed {
		t.Fatalf("exact repeat regenerated or skipped checks: %+v %v calls=%d/%d", second, err, provider.counts, provider.generations)
	}
	for _, review := range []string{"unreviewed", "assistant_reviewed", "assistant_reviewed"} {
		receipt, err := Record(testContext, broker, plan, second, review)
		if err != nil || receipt.Artifact.Version != 1 {
			t.Fatalf("record %s: %+v %v", review, receipt, err)
		}
	}
	index, err := ReadIndex(testContext, broker, plan.ReferenceID)
	if err != nil || len(index.Entries) != 2 {
		t.Fatalf("index append/repeat: %+v %v", index, err)
	}
	entries, err := ReadEntries(testContext, broker, plan.ReferenceID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("metadata resolution: %+v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.HumanAcceptance != "not_asserted" || entry.ReferenceSHA256 != plan.ReferenceSHA256 || entry.EvidenceSHA256 != plan.EvidenceSHA256 || entry.AnswerSHA256 != Hash([]byte(passingText())) || entry.AnswerVersion < 2 {
			t.Fatalf("metadata provenance/acceptance mismatch: %+v", entry)
		}
		data, _ := json.Marshal(entry)
		for _, private := range []string{passingText(), "Maintains synthetic", "sample company builds", "What interests you"} {
			if strings.Contains(string(data), private) {
				t.Fatal("index metadata retained prose or profile content")
			}
		}
	}
	changedEvidence := []byte(strings.Replace(string(evidenceBytes(t)), "infrastructure tools", "different current tools", 1))
	changed, err := Build(referenceBytes(t), changedEvidence, plan.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	third, err := router.Run(testContext, changed.Request)
	if err != nil || provider.generations != 2 || third.Attempts[0].Result.Reused || third.Attempts[0].Result.Fingerprint == first.Attempts[0].Result.Fingerprint {
		t.Fatalf("changed facts silently reused old answer: %+v %v", third, err)
	}
	if _, err := Record(testContext, broker, changed, third, "unreviewed"); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadEntries(testContext, broker, plan.ReferenceID)
	if err != nil || len(entries) != 3 || entries[0].EvidenceSHA256 != plan.EvidenceSHA256 || entries[2].EvidenceSHA256 != changed.EvidenceSHA256 {
		t.Fatalf("historical source versions not preserved: %+v %v", entries, err)
	}
}

func Test193WordGenerationCannotBeRecordedAsReviewed(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	provider := &fixtureProvider{text: "company platform " + strings.Repeat("synthetic ", 95) + "\n\n" + strings.Repeat("example ", 96)}
	result, err := fixtureRouter(broker, provider).Run(testContext, plan.Request)
	if !errors.Is(err, delegation.ErrQualityFailed) || result.Response != nil || result.Attempts[0].QualityPassed || *result.Attempts[0].Checks[0].Observed != 193 || provider.generations != 1 {
		t.Fatalf("oversized prose passed: %+v %v", result, err)
	}
	if _, err := Record(testContext, broker, plan, result, "assistant_reviewed"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("rejected generation indexed as reviewed: %v", err)
	}
	index, err := ReadIndex(testContext, broker, plan.ReferenceID)
	if err != nil || len(index.Entries) != 0 {
		t.Fatalf("rejected answer created index: %+v %v", index, err)
	}
}

type brokerHooks struct {
	inference.Broker
	get     func(context.Context, string) (protocol.Artifact, error)
	publish func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error)
}

func (b brokerHooks) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	if b.get != nil {
		return b.get(ctx, key)
	}
	return b.Broker.Get(ctx, key)
}

func (b brokerHooks) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	if b.publish != nil {
		return b.publish(ctx, request)
	}
	return b.Broker.Publish(ctx, request)
}

func TestRecordRejectsFakeAcceptanceAndTamperedResultOrJob(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	result, err := fixtureRouter(broker, &fixtureProvider{text: passingText()}).Run(testContext, plan.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, review := range []string{"accepted", "human_accepted", "person_reviewed", ""} {
		if _, err := Record(testContext, broker, plan, result, review); !errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("invented review accepted: %q %v", review, err)
		}
	}
	for _, change := range []func(*delegation.Result){
		func(r *delegation.Result) { r.RequestSHA256 = Hash([]byte("other request")) },
		func(r *delegation.Result) { r.Status = "quality_failed" },
		func(r *delegation.Result) { r.Response.Complete = false },
		func(r *delegation.Result) { r.Attempts[0].Result.Durable = false },
		func(r *delegation.Result) { r.Attempts[0].QualityPassed = false },
		func(r *delegation.Result) { r.Response.Text = strings.Replace(passingText(), "clear", "altered", 1) },
	} {
		data, _ := json.Marshal(result)
		var tampered delegation.Result
		if err := json.Unmarshal(data, &tampered); err != nil {
			t.Fatal(err)
		}
		change(&tampered)
		if _, err := Record(testContext, broker, plan, tampered, "assistant_reviewed"); err == nil {
			t.Fatal("tampered result was recorded")
		}
	}
	jobKey := "inference/v1/jobs/" + result.Attempts[0].Result.Fingerprint
	job, err := broker.Get(testContext, jobKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tamper := range []func(*protocol.Artifact){
		func(a *protocol.Artifact) { a.Key += "-wrong" },
		func(a *protocol.Artifact) { a.Version = 0 },
		func(a *protocol.Artifact) {
			a.Content = strings.Replace(a.Content, `"state":"complete"`, `"state":"pending"`, 1)
		},
		func(a *protocol.Artifact) {
			a.Content = strings.Replace(a.Content, `"schema":1`, `"schema":1,"schema":1`, 1)
		},
	} {
		changed := job
		tamper(&changed)
		publishes := 0
		wrapped := brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
			if key == jobKey {
				return changed, nil
			}
			return broker.Get(ctx, key)
		}, publish: func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
			publishes++
			return protocol.PublishResult{}, errors.New("unexpected publication")
		}}
		if _, err := Record(testContext, wrapped, plan, result, "assistant_reviewed"); err == nil || publishes != 0 {
			t.Fatalf("tampered durable job accepted or published: %v calls=%d", err, publishes)
		}
	}
}

func TestRecordCannotRebindAnotherSnapshotOrLegacyJob(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	changedEvidence := []byte(strings.Replace(string(evidenceBytes(t)), "infrastructure tools", "a separate product", 1))
	otherPlan, err := Build(referenceBytes(t), changedEvidence, plan.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	router := fixtureRouter(broker, &fixtureProvider{text: passingText()})
	other, err := router.Run(testContext, otherPlan.Request)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the complete durable job for another task, but replace the public
	// result's request digest. Broker-side request provenance must catch this.
	other.RequestSHA256 = plan.RequestSHA256
	if _, err := Record(testContext, broker, plan, other, "assistant_reviewed"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("other snapshot rebound to current writing request: %v", err)
	}
	result, err := router.Run(testContext, plan.Request)
	if err != nil {
		t.Fatal(err)
	}
	jobKey := "inference/v1/jobs/" + result.Attempts[0].Result.Fingerprint
	job, err := broker.Get(testContext, jobKey)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(job.Content), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw["request_fingerprint"]) == 0 {
		t.Fatal("new runner records omitted approved-request provenance")
	}
	delete(raw, "request_fingerprint")
	legacy, _ := json.Marshal(raw)
	job.Content = string(legacy)
	wrapped := brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
		if key == jobKey {
			return job, nil
		}
		return broker.Get(ctx, key)
	}}
	if _, err := Record(testContext, wrapped, plan, result, "assistant_reviewed"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("legacy job invented request provenance: %v", err)
	}
	index, err := ReadIndex(testContext, broker, plan.ReferenceID)
	if err != nil || len(index.Entries) != 0 {
		t.Fatalf("unconfirmed provenance created historical entries: %+v %v", index, err)
	}
}

func TestRecordRequiresCanonicalPersistedProvenanceAndResponse(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	result, err := fixtureRouter(broker, &fixtureProvider{text: passingText()}).Run(testContext, plan.Request)
	if err != nil {
		t.Fatal(err)
	}
	jobKey := "inference/v1/jobs/" + result.Attempts[0].Result.Fingerprint
	job, err := broker.Get(testContext, jobKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schema", "state", "fingerprint", "request_fingerprint", "response", "complete", "text", "model", "usage", "input_tokens"} {
		t.Run(field, func(t *testing.T) {
			changed := job
			old := `"` + field + `":`
			new := `"` + strings.ToUpper(field) + `":`
			changed.Content = strings.Replace(job.Content, old, new, 1)
			if changed.Content == job.Content {
				t.Fatalf("fixture omitted field %q", field)
			}
			publishes := 0
			wrapped := brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
				if key == jobKey {
					return changed, nil
				}
				return broker.Get(ctx, key)
			}, publish: func(context.Context, protocol.PublishRequest) (protocol.PublishResult, error) {
				publishes++
				return protocol.PublishResult{}, errors.New("unexpected publication")
			}}
			if _, err := Record(testContext, wrapped, plan, result, "assistant_reviewed"); !errors.Is(err, ErrUnconfirmed) || publishes != 0 {
				t.Fatalf("sole case variant %q accepted or published: %v writes=%d", field, err, publishes)
			}
		})
	}
}

func TestRecordRejectsMalformedImmutableReceiptBeforeIndexing(t *testing.T) {
	broker, plan := realBroker(t), fixturePlan(t)
	result, err := fixtureRouter(broker, &fixtureProvider{text: passingText()}).Run(testContext, plan.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		tamper func(*protocol.PublishResult)
	}{
		{"key", func(r *protocol.PublishResult) { r.Artifact.Key += "-wrong" }},
		{"version", func(r *protocol.PublishResult) { r.Artifact.Version++ }},
		{"content", func(r *protocol.PublishResult) { r.Artifact.Content += " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publishes, indexReads := 0, 0
			wrapped := brokerHooks{Broker: broker, get: func(ctx context.Context, key string) (protocol.Artifact, error) {
				if strings.HasPrefix(key, indexPrefix) {
					indexReads++
				}
				return broker.Get(ctx, key)
			}, publish: func(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
				publishes++
				if !strings.HasPrefix(request.Key, entryPrefix) {
					t.Fatalf("malformed metadata receipt allowed index publication: %s", request.Key)
				}
				receipt, err := broker.Publish(ctx, request)
				if err == nil {
					tc.tamper(&receipt)
				}
				return receipt, err
			}}
			if _, err := Record(testContext, wrapped, plan, result, "assistant_reviewed"); !errors.Is(err, ErrUnconfirmed) || publishes != 1 || indexReads != 0 {
				t.Fatalf("malformed receipt advanced index: %v writes=%d index reads=%d", err, publishes, indexReads)
			}
		})
	}
	index, err := ReadIndex(testContext, broker, plan.ReferenceID)
	if err != nil || len(index.Entries) != 0 {
		t.Fatalf("malformed receipt created reviewed index: %+v %v", index, err)
	}
	// An explicit retry with the real receipt can reconcile the confirmed,
	// immutable metadata publication without generating the answer again.
	if _, err := Record(testContext, broker, plan, result, "assistant_reviewed"); err != nil {
		t.Fatalf("explicit reconciliation failed: %v", err)
	}
}

func fixtureEntry(n int) Entry {
	return Entry{Schema: 1, RequestSHA256: Hash([]byte(fmt.Sprintf("request-%d", n))), ReferenceID: "sample-profile", ReferenceRevision: "profile-1", ReferenceSHA256: Hash([]byte("reference")), EvidenceID: "sample-role", EvidenceRevision: "evidence-1", EvidenceSHA256: Hash([]byte("evidence")), AnswerKey: "inference/v1/jobs/" + Hash([]byte(fmt.Sprintf("answer-%d", n))), AnswerVersion: 2, AnswerSHA256: Hash([]byte("answer text omitted")), ReviewStatus: "assistant_reviewed", HumanAcceptance: "not_asserted"}
}

func publishEntry(t *testing.T, broker inference.Broker, entry Entry) EntryRef {
	t.Helper()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	ref := EntryRef{Key: entryKey(entry), Version: 1, SHA256: Hash(data)}
	if _, err := broker.Publish(testContext, protocol.PublishRequest{Key: ref.Key, OperationID: "entry-fixture-" + Hash(data), Content: string(data)}); err != nil {
		t.Fatal(err)
	}
	return ref
}
