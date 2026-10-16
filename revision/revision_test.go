package revision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/outcomes"
	"github.com/spfuzzylink/questlock/protocol"
)

var testContext = context.Background()

func textPointer(text string) *string { return &text }
func fixtureRequest() Request {
	text := "Questlock works. Questlock saves repeated work."
	return Request{Schema: Schema, Task: "launch-copy", Revision: "bold-name-1", Source: Source{Text: &text, SHA256: Digest(text)}, Edits: []Edit{{Old: "Questlock", New: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤", ExpectedMatches: 2}}, SourceChecks: []evaluation.Check{{ID: "reviewed-name", Kind: "contains", Text: "Questlock works."}}, Checks: []evaluation.Check{{ID: "bold-name", Kind: "contains", Text: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 works."}, {ID: "claims", Kind: "contains", Text: "saves repeated work."}}}
}

type memoryBroker struct {
	mu              sync.Mutex
	artifacts       map[string]protocol.Artifact
	operations      map[string]protocol.PublishResult
	gets, publishes int
}

func newMemoryBroker() *memoryBroker {
	return &memoryBroker{artifacts: map[string]protocol.Artifact{}, operations: map[string]protocol.PublishResult{}}
}
func (b *memoryBroker) Get(_ context.Context, key string) (protocol.Artifact, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	if artifact, ok := b.artifacts[key]; ok {
		return artifact, nil
	}
	return protocol.Artifact{}, &client.Error{Code: "not_found", Status: 404}
}
func (b *memoryBroker) Publish(_ context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishes++
	if result, exists := b.operations[request.OperationID]; exists {
		result.Replayed = true
		return result, nil
	}
	current := b.artifacts[request.Key]
	if current.Version != request.ExpectedVersion {
		return protocol.PublishResult{}, &client.Error{Code: "version_conflict", Status: 409}
	}
	artifact := protocol.Artifact{Scope: "fixture", Key: request.Key, Version: current.Version + 1, Content: request.Content}
	b.artifacts[request.Key] = artifact
	result := protocol.PublishResult{Artifact: artifact}
	b.operations[request.OperationID] = result
	return result, nil
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
func (b brokerHooks) Publish(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
	if b.publish != nil {
		return b.publish(ctx, req)
	}
	return b.Broker.Publish(ctx, req)
}

func assertZeroGeneration(t *testing.T, result Result) {
	t.Helper()
	if result.GenerationCalled || result.ThisRunUsage != (inference.Usage{}) || result.ProviderCostUSD != 0 {
		t.Fatalf("unexpected generation/accounting: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"avoided_usage", "avoided_cost", "accepted", "generation_runtime_cache", "generation_timing"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("invented metadata %s", forbidden)
		}
	}
}

func TestPlanIsCheckedAndHasNoPublicationOrOutcome(t *testing.T) {
	request := fixtureRequest()
	request.TrackOutcome = true
	result, err := (Runner{}).Plan(testContext, request)
	if err != nil || result.Status != "planned" || result.Durable || result.Reused || result.Artifact != nil || result.OutcomeTracking != nil || result.Text != "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 works. 𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 saves repeated work." || result.OutputSHA256 != Digest(result.Text) {
		t.Fatalf("plan mismatch: %+v %v", result, err)
	}
	assertZeroGeneration(t, result)
	if result.CostBasis != "deterministic_edit" || !passingEvidence(result.SourceChecks) || !passingEvidence(result.Checks) {
		t.Fatal("missing checked edit evidence")
	}
	request.TrackOutcome = false
	without, err := RequestDigest(request)
	if err != nil || without != result.RequestSHA256 {
		t.Fatal("tracking changed work identity")
	}
}

func TestRunAndExactReplayPublishOnlyImmutableContent(t *testing.T) {
	broker := newMemoryBroker()
	runner := Runner{Broker: broker}
	request := fixtureRequest()
	first, err := runner.Run(testContext, request)
	if err != nil || first.Status != "completed" || !first.Durable || first.Reused || first.Artifact == nil || first.Artifact.Version != 1 || first.Artifact.Key != ResultPrefix+first.RequestSHA256 || first.Artifact.SHA256 != first.OutputSHA256 {
		t.Fatalf("fresh: %+v %v", first, err)
	}
	second, err := runner.Run(testContext, request)
	if err != nil || !second.Durable || !second.Reused || second.Text != first.Text || second.CostBasis != "reuse_zero" {
		t.Fatalf("replay: %+v %v", second, err)
	}
	assertZeroGeneration(t, first)
	assertZeroGeneration(t, second)
	if broker.artifacts[first.Artifact.Key].Version != 1 || broker.publishes != 2 {
		t.Fatal("immutable result overwritten")
	}
	// A conflict from an independent worker can reuse only an exact payload.
	broker.operations = map[string]protocol.PublishResult{}
	third, err := runner.Run(testContext, request)
	if err != nil || !third.Reused || third.Text != first.Text || broker.artifacts[first.Artifact.Key].Version != 1 {
		t.Fatalf("CAS reuse failed: %+v %v", third, err)
	}
}

func TestFailureWithholdsTextBeforeAnyPublication(t *testing.T) {
	cases := map[string]struct {
		change func(*Request)
		want   error
	}{
		"source hash drift":  {func(r *Request) { r.Source.SHA256 = Digest("changed") }, ErrSourceMismatch},
		"count drift":        {func(r *Request) { r.Edits[0].ExpectedMatches = 1 }, ErrCountMismatch},
		"source checks fail": {func(r *Request) { r.SourceChecks[0].Text = "missing source text" }, ErrQualityFailed},
		"output checks fail": {func(r *Request) { r.Checks[0].Text = "missing output text" }, ErrQualityFailed},
		"empty output":       {func(r *Request) { r.Edits = []Edit{{Old: *r.Source.Text, New: "", ExpectedMatches: 1}} }, ErrInvalid},
		"net no op": {func(r *Request) {
			r.Edits = []Edit{{Old: "Questlock", New: "Other", ExpectedMatches: 2}, {Old: "Other", New: "Questlock", ExpectedMatches: 2}}
		}, ErrInvalid},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			r := fixtureRequest()
			test.change(&r)
			b := newMemoryBroker()
			result, err := (Runner{Broker: b}).Run(testContext, r)
			if !errors.Is(err, test.want) || result.Text != "" || result.Durable || result.Artifact != nil || b.publishes != 0 {
				t.Fatalf("failure leaked or published: %+v %v", result, err)
			}
			assertZeroGeneration(t, result)
		})
	}
}

func TestSequentialNonoverlappingEditsAndJSONChecks(t *testing.T) {
	request := fixtureRequest()
	request.Source.Text = textPointer(`{"name":"Questlock","items":"aaaa"}`)
	request.Source.SHA256 = Digest(*request.Source.Text)
	request.SourceChecks = []evaluation.Check{{ID: "valid-source", Kind: "json_valid"}}
	request.Edits = []Edit{{Old: "Questlock", New: "Questlock v0.6.0", ExpectedMatches: 1}, {Old: "aa", New: "b", ExpectedMatches: 2}, {Old: "bb", New: "done", ExpectedMatches: 1}}
	request.Checks = []evaluation.Check{{ID: "valid-output", Kind: "json_valid"}, {ID: "value", Kind: "json_equals", Pointer: "/items", Expected: json.RawMessage(`"done"`)}}
	result, err := (Runner{}).Plan(testContext, request)
	if err != nil || result.Text != `{"name":"Questlock v0.6.0","items":"done"}` {
		t.Fatalf("sequence: %+v %v", result, err)
	}
	request.Edits[1].ExpectedMatches = 3
	if result, err := (Runner{}).Plan(testContext, request); !errors.Is(err, ErrCountMismatch) || result.Text != "" {
		t.Fatal("overlapping matches admitted")
	}
}

func TestBoundsAccountForIntermediateExpansionAndJSONEscaping(t *testing.T) {
	for _, test := range []struct {
		name, source, replacement string
		count                     int
	}{
		{"raw expansion", strings.Repeat("a", 64), strings.Repeat("b", MaxReplacementBytes), 64},
		// Raw output is exactly 256KiB, but JSON escapes each '<' to six bytes.
		{"encoded artifact expansion", strings.Repeat("a", 4096), strings.Repeat("<", 64), 4096},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := fixtureRequest()
			request.Source.Text = &test.source
			request.Source.SHA256 = Digest(test.source)
			request.SourceChecks = []evaluation.Check{{ID: "a", Kind: "contains", Text: "a"}}
			request.Edits = []Edit{{Old: "a", New: test.replacement, ExpectedMatches: test.count}}
			request.Checks = []evaluation.Check{{ID: "out", Kind: "contains", Text: test.replacement[:1]}}
			b := newMemoryBroker()
			result, err := (Runner{Broker: b}).Run(testContext, request)
			if !errors.Is(err, ErrInvalid) || result.Text != "" || b.publishes != 0 {
				t.Fatalf("expansion escaped bound: %+v %v", result, err)
			}
		})
	}
	request := fixtureRequest()
	request.Source.Text = textPointer(strings.Repeat("<", MaxTextBytes))
	request.Source.SHA256 = Digest(*request.Source.Text)
	if err := Validate(request); !errors.Is(err, ErrInvalid) {
		t.Fatal("encoded request expansion admitted")
	}
	request = fixtureRequest()
	request.Source.Text = textPointer(strings.Repeat("x", MaxTextBytes+1))
	if err := Validate(request); !errors.Is(err, ErrInvalid) {
		t.Fatal("source byte cap ignored")
	}
}

func TestLoadRejectsAmbiguousOrUnknownJSON(t *testing.T) {
	encoded, _ := json.Marshal(fixtureRequest())
	good := string(encoded)
	if _, err := Load(strings.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"root duplicate":       strings.Replace(good, `"schema":1`, `"schema":1,"schema":1`, 1),
		"escaped duplicate":    strings.Replace(good, `"schema":1`, `"schema":1,"\u0073chema":1`, 1),
		"unknown":              strings.Replace(good, `"schema":1`, `"schema":1,"model":"free"`, 1),
		"case alias":           strings.Replace(good, `"task":`, `"Task":`, 1),
		"nested duplicate":     strings.Replace(good, `"expected_matches":2`, `"expected_matches":2,"expected_matches":1`, 1),
		"nested unknown":       strings.Replace(good, `"old":"Questlock"`, `"old":"Questlock","ignore_checks":true`, 1),
		"null boolean":         strings.Replace(good, `"schema":1`, `"schema":1,"track_outcome":null`, 1),
		"missing new":          strings.Replace(good, `"new":"𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤",`, "", 1),
		"null new":             strings.Replace(good, `"new":"𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤"`, `"new":null`, 1),
		"null optional scalar": strings.Replace(good, `"source":{`, `"source":{"version":null,`, 1),
		"trailing":             good + ` {}`,
		"invalid utf8":         good + string([]byte{0xff}),
		"oversize":             good + strings.Repeat(" ", MaxRequestBytes),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(data)); !errors.Is(err, ErrInvalid) {
				t.Fatal("ambiguous request accepted")
			}
		})
	}
}

func TestExplicitEmptyReplacementDeletesOnlyRequestedLiteral(t *testing.T) {
	request := fixtureRequest()
	request.Source.Text = textPointer("remove Questlock works.")
	request.Source.SHA256 = Digest(*request.Source.Text)
	request.SourceChecks = []evaluation.Check{{ID: "source", Kind: "contains", Text: "remove Questlock"}}
	request.Edits = []Edit{{Old: "remove ", New: "", ExpectedMatches: 1}}
	request.Checks = []evaluation.Check{{ID: "remaining", Kind: "contains", Text: "Questlock works."}, {ID: "removed", Kind: "not_contains", Text: "remove "}}
	encoded, _ := json.Marshal(request)
	loaded, err := Load(strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("explicit deletion rejected: %v", err)
	}
	result, err := (Runner{}).Plan(testContext, loaded)
	if err != nil || result.Text != "Questlock works." {
		t.Fatalf("explicit bounded deletion: %+v %v", result, err)
	}
}

func TestInvalidRequestLimitsAndPredicates(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"unknown schema": func(r *Request) { r.Schema = 2 },
		"blank task":     func(r *Request) { r.Task = "" },
		"bad utf8":       func(r *Request) { r.Source.Text = textPointer(string([]byte{0xff})) },
		"mixed source":   func(r *Request) { r.Source.Key = ResultPrefix + Digest("request"); r.Source.Version = 1 },
		"arbitrary source": func(r *Request) {
			r.Source.Text = nil
			r.Source.Key = "inference/v1/jobs/" + Digest("request")
			r.Source.Version = 1
		},
		"mutable version": func(r *Request) {
			r.Source.Text = nil
			r.Source.Key = ResultPrefix + Digest("request")
			r.Source.Version = 2
		},
		"uppercase hash":    func(r *Request) { r.Source.SHA256 = strings.ToUpper(r.Source.SHA256) },
		"empty old":         func(r *Request) { r.Edits[0].Old = "" },
		"unchanged":         func(r *Request) { r.Edits[0].New = r.Edits[0].Old },
		"zero count":        func(r *Request) { r.Edits[0].ExpectedMatches = 0 },
		"excess count":      func(r *Request) { r.Edits[0].ExpectedMatches = 4097 },
		"excess edits":      func(r *Request) { r.Edits = make([]Edit, 33) },
		"replacement cap":   func(r *Request) { r.Edits[0].New = strings.Repeat("x", MaxReplacementBytes+1) },
		"no source checks":  func(r *Request) { r.SourceChecks = nil },
		"duplicate checks":  func(r *Request) { r.Checks = append(r.Checks, r.Checks[0]) },
		"unsupported check": func(r *Request) { r.Checks[0].Kind = "semantic_quality" },
		"ambiguous expected": func(r *Request) {
			r.Checks = []evaluation.Check{{ID: "json", Kind: "json_equals", Expected: json.RawMessage(`{"x":1,"x":2}`)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := fixtureRequest()
			change(&request)
			if err := Validate(request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid request admitted: %v", err)
			}
		})
	}
}

func childRequest(parent Result) Request {
	request := fixtureRequest()
	request.Revision = "add-mission-1"
	request.Source = Source{Key: parent.Artifact.Key, Version: parent.Artifact.Version, SHA256: parent.OutputSHA256}
	request.SourceChecks = []evaluation.Check{{ID: "parent", Kind: "contains", Text: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 works."}}
	request.Edits = []Edit{{Old: "works.", New: "works!", ExpectedMatches: 1}}
	request.Checks = []evaluation.Check{{ID: "edited", Kind: "contains", Text: "𝐐𝐮𝐞𝐬𝐭𝐥𝐨𝐜𝐤 works!"}}
	return request
}

func TestReferencedParentRequiresPinnedContentVersionAndEvidence(t *testing.T) {
	for _, tamper := range []string{"none", "text", "request hash", "evidence", "source provenance", "unknown field", "version", "key", "trailing"} {
		t.Run(tamper, func(t *testing.T) {
			broker := newMemoryBroker()
			runner := Runner{Broker: broker}
			parent, err := runner.Run(testContext, fixtureRequest())
			if err != nil {
				t.Fatal(err)
			}
			request := childRequest(parent)
			artifact := broker.artifacts[parent.Artifact.Key]
			var payload record
			if err := json.Unmarshal([]byte(artifact.Content), &payload); err != nil {
				t.Fatal(err)
			}
			switch tamper {
			case "text":
				payload.Text = "forged text"
				payload.OutputSHA256 = Digest(payload.Text)
			case "request hash":
				payload.RequestSHA256 = Digest("other")
			case "evidence":
				payload.Checks[0].Passed = false
			case "source provenance":
				payload.Source = &Ref{Key: ResultPrefix + Digest("source"), Version: 1, SHA256: Digest("wrong")}
			case "version":
				artifact.Version = 2
			case "key":
				artifact.Key = ResultPrefix + Digest("wrong")
			}
			encoded, _ := json.Marshal(payload)
			artifact.Content = string(encoded)
			if tamper == "unknown field" {
				artifact.Content = strings.TrimSuffix(artifact.Content, "}") + `,"accepted":true}`
			}
			if tamper == "trailing" {
				artifact.Content += "{}"
			}
			broker.artifacts[parent.Artifact.Key] = artifact
			result, err := runner.Run(testContext, request)
			if tamper == "none" {
				if err != nil || !result.Durable || result.Text == "" {
					t.Fatalf("valid parent rejected: %+v %v", result, err)
				}
				return
			}
			if !errors.Is(err, ErrSourceMismatch) || result.Text != "" || result.Durable || broker.publishes != 1 {
				t.Fatalf("tampered parent used: %+v %v", result, err)
			}
		})
	}
}

func TestPublicationUncertaintyAndMismatchedReceiptsWithholdOutput(t *testing.T) {
	for _, mode := range []string{"lost acknowledgement", "mismatched receipt", "conflicting content", "different valid record", "missing after conflict"} {
		t.Run(mode, func(t *testing.T) {
			broker := newMemoryBroker()
			calls := 0
			hook := brokerHooks{Broker: broker, publish: func(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
				calls++
				switch mode {
				case "lost acknowledgement":
					_, _ = broker.Publish(ctx, req)
					return protocol.PublishResult{}, errors.New("secret-provider-token-must-not-leak")
				case "mismatched receipt":
					result, err := broker.Publish(ctx, req)
					result.Artifact.Version = 2
					return result, err
				case "conflicting content":
					req.Content = strings.Replace(req.Content, "saves repeated work.", "makes unverifiable claims.", 1)
					_, _ = broker.Publish(ctx, req)
					return protocol.PublishResult{}, &client.Error{Code: "version_conflict"}
				case "different valid record":
					var other record
					if err := json.Unmarshal([]byte(req.Content), &other); err != nil {
						t.Fatal(err)
					}
					other.Checks[0].ID = "different-checked-predicate"
					encoded, _ := json.Marshal(other)
					req.Content = string(encoded)
					_, _ = broker.Publish(ctx, req)
					return protocol.PublishResult{}, &client.Error{Code: "version_conflict"}
				default:
					return protocol.PublishResult{}, &client.Error{Code: "version_conflict"}
				}
			}}
			result, err := (Runner{Broker: hook}).Run(testContext, fixtureRequest())
			if !errors.Is(err, ErrPublication) || result.Text != "" || result.Durable || result.Artifact != nil || calls != 1 || strings.Contains(err.Error(), "secret-provider") {
				t.Fatalf("uncertain publication exposed/retried: %+v %v", result, err)
			}
			if mode == "lost acknowledgement" {
				next, nextErr := (Runner{Broker: broker}).Run(testContext, fixtureRequest())
				if nextErr != nil || !next.Reused || !next.Durable {
					t.Fatalf("explicit later reconciliation failed: %+v %v", next, nextErr)
				}
			}
		})
	}
}

func TestHTTPSharedWorkersScopeIsolationAndConcurrentNoOverwrite(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(httpapi.New(db))
	defer server.Close()
	clients := make([]*client.Client, 3)
	for i, scope := range []string{"team", "team", "other"} {
		token, err := db.CreateToken(testContext, fmt.Sprintf("worker-%d", i), scope)
		if err != nil {
			t.Fatal(err)
		}
		clients[i], err = client.New(server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
	}
	parent, err := (Runner{Broker: clients[0]}).Run(testContext, fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	child := childRequest(parent)
	if hidden, err := (Runner{Broker: clients[2]}).Run(testContext, child); !errors.Is(err, ErrSourceMismatch) || hidden.Text != "" {
		t.Fatal("parent leaked across broker scope")
	}
	const workers = 12
	results := make(chan Result, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Go(func() {
			result, err := (Runner{Broker: clients[1]}).Run(testContext, child)
			results <- result
			errs <- err
		})
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fresh := 0
	for result := range results {
		if !result.Durable || result.Artifact == nil || result.Artifact.Version != 1 {
			t.Fatal("race mutated immutable record")
		}
		if !result.Reused {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("wanted one fresh immutable commit, got %d", fresh)
	}
	replay, err := (Runner{Broker: clients[0]}).Run(testContext, child)
	if err != nil || !replay.Reused || !replay.Durable {
		t.Fatalf("shared exact reuse failed: %+v %v", replay, err)
	}
}

func TestOutcomeAccountingAndFailureRecoveryNeverRegeneratesOrAccepts(t *testing.T) {
	broker := newMemoryBroker()
	request := fixtureRequest()
	request.TrackOutcome = true
	runner := Runner{Broker: broker}
	first, err := runner.Run(testContext, request)
	if err != nil || first.OutcomeTracking == nil || first.OutcomeTracking.Status != "recorded" {
		t.Fatalf("tracking: %+v %v", first, err)
	}
	second, err := runner.Run(testContext, request)
	if err != nil || !second.Reused || second.OutcomeTracking.RunID == first.OutcomeTracking.RunID {
		t.Fatal("invocation ID reused")
	}
	task := outcomes.Task{ID: request.Task, Revision: request.Revision}
	snapshot, err := (outcomes.Store{Broker: broker}).Get(testContext, task)
	if err != nil || snapshot.State.Status != "unreviewed" || len(snapshot.Runs) != 2 {
		t.Fatalf("checks inferred acceptance or runs lost: %+v %v", snapshot, err)
	}
	for i, run := range snapshot.Runs {
		attempt := run.Attempts[0]
		if attempt.GenerationCalled || !attempt.UsageKnown || attempt.Usage != (inference.Usage{}) || attempt.EstimatedCostUSD == nil || *attempt.EstimatedCostUSD != 0 {
			t.Fatal("wrong zero generation accounting")
		}
		if i == 0 && attempt.CostBasis != "deterministic_edit" || i == 1 && attempt.CostBasis != "reuse_zero" {
			t.Fatal("wrong operation basis")
		}
	}
	request.Revision = "failed-check"
	request.Checks[0].Text = "missing output"
	failed, err := runner.Run(testContext, request)
	if !errors.Is(err, ErrQualityFailed) || failed.Text != "" || failed.OutcomeTracking.Status != "recorded" {
		t.Fatalf("failed attempt not retained: %+v %v", failed, err)
	}
	failedLedger, err := (outcomes.Store{Broker: broker}).Get(testContext, outcomes.Task{ID: request.Task, Revision: request.Revision})
	if err != nil || failedLedger.Runs[0].Status != "quality_failed" || failedLedger.Runs[0].Attempts[0].Durable {
		t.Fatal("failed checks advertised durable output")
	}
	request = fixtureRequest()
	request.Revision = "accounting-failure"
	request.TrackOutcome = true
	failing := brokerHooks{Broker: broker, publish: func(ctx context.Context, req protocol.PublishRequest) (protocol.PublishResult, error) {
		if strings.HasPrefix(req.Key, "outcomes/") {
			return protocol.PublishResult{}, errors.New("uncertain accounting")
		}
		return broker.Publish(ctx, req)
	}}
	result, err := (Runner{Broker: failing}).Run(testContext, request)
	if err != nil || !result.Durable || result.Text == "" || result.OutcomeTracking.Status != "failed" || result.OutcomeTracking.Record == nil {
		t.Fatalf("metadata failure changed checked answer: %+v %v", result, err)
	}
	before := broker.publishes
	tracking := result.OutcomeTracking
	if _, err := (outcomes.Store{Broker: broker}).Record(testContext, tracking.Task, *tracking.Record); err != nil {
		t.Fatal(err)
	}
	if broker.publishes != before+1 {
		t.Fatal("reconciliation repeated result publication")
	}
}

func TestCanceledAndUnpublishedRunsDoNotExposeOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	broker := newMemoryBroker()
	result, err := (Runner{Broker: broker}).Run(ctx, fixtureRequest())
	if !errors.Is(err, context.Canceled) || result.Text != "" || broker.publishes != 0 {
		t.Fatal("canceled request progressed")
	}
	result, err = (Runner{}).Run(testContext, fixtureRequest())
	if !errors.Is(err, ErrInvalid) || result.Text != "" || result.Durable {
		t.Fatal("unpublished output exposed")
	}
}

func TestCancellationDuringConfirmedPublicationRetainsReceiptButWithholdsText(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext)
	broker := newMemoryBroker()
	hooked := brokerHooks{Broker: broker, publish: func(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
		result, err := broker.Publish(ctx, request)
		if strings.HasPrefix(request.Key, ResultPrefix) {
			cancel()
		}
		return result, err
	}}
	request := fixtureRequest()
	request.TrackOutcome = true
	result, err := (Runner{Broker: hooked}).Run(ctx, request)
	if !errors.Is(err, context.Canceled) || result.Status != "stopped" || result.Text != "" || !result.Durable || result.Artifact == nil || result.OutcomeTracking == nil || result.OutcomeTracking.Status != "recorded" {
		t.Fatalf("canceled publication mishandled: %+v %v", result, err)
	}
	ledger, err := (outcomes.Store{Broker: broker}).Get(testContext, result.OutcomeTracking.Task)
	if err != nil || len(ledger.Runs) != 1 || ledger.Runs[0].Status != "stopped" || !ledger.Runs[0].Attempts[0].Durable || !ledger.Runs[0].Attempts[0].CheckPassed {
		t.Fatalf("confirmed accounting lost: %+v %v", ledger, err)
	}
	assertZeroGeneration(t, result)
}
