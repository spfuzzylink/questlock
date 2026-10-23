package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
	"github.com/spfuzzylink/questlock/retention"
	"github.com/spfuzzylink/questlock/revision"
)

// These tests run the public runners over the actual broker HTTP API. Their
// provider is an in-process deterministic fixture with no model or paid route.
type storageReuseFixture struct {
	dbPath    string
	policy    retention.Config
	store     *store.Store
	server    *httptest.Server
	client    *client.Client
	token     string
	principal store.Principal
}

func newStorageReuseFixture(t *testing.T) *storageReuseFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &storageReuseFixture{dbPath: filepath.Join(dir, "broker.db"), policy: retention.DefaultConfig(filepath.Join(dir, "cold"))}
	f.policy.Adaptive, f.policy.HotHistoryDays, f.policy.KeepRecentVersions, f.policy.ArchiveCompletedCurrent = false, 0, 1, true
	f.store, err = store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.server != nil {
			f.server.Close()
		}
		if f.store != nil {
			_ = f.store.Close()
		}
	})
	f.token, err = f.store.CreateToken(t.Context(), "reuse-worker", "reuse-scope")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.StorageConfigure(t.Context(), f.policy); err != nil {
		t.Fatal(err)
	}
	f.serve(t)
	return f
}

func (f *storageReuseFixture) serve(t *testing.T) {
	t.Helper()
	var err error
	f.principal, err = f.store.Authenticate(t.Context(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(httpapi.New(f.store))
	f.client, err = client.New(f.server.URL, f.token)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *storageReuseFixture) restart(t *testing.T) {
	t.Helper()
	f.server.Close()
	f.server = nil
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store = nil
	var err error
	f.store, err = store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.serve(t)
}

func (f *storageReuseFixture) archive(t *testing.T) store.StorageReport {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
	report, err := f.store.StorageArchive(t.Context(), false, now, retention.Plan(f.policy, admission.Snapshot{}, now))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *storageReuseFixture) currentIsCold(t *testing.T, key string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cold bool
	if err := db.QueryRowContext(t.Context(), `SELECT v.content IS NULL FROM artifacts a JOIN artifact_versions v ON v.scope=a.scope AND v.key=a.key AND v.version=a.current_version WHERE a.scope=? AND a.key=?`, f.principal.Scope, key).Scan(&cold); err != nil {
		t.Fatal(err)
	}
	return cold
}

func (f *storageReuseFixture) archivedFile(t *testing.T, key string, version int64) string {
	t.Helper()
	db, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var digest string
	if err := db.QueryRowContext(t.Context(), "SELECT sha256 FROM cold_payloads WHERE scope=? AND key=? AND version=?", f.principal.Scope, key, version).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f.policy.ColdDir, digest+".gzip")
}

type storageReuseProvider struct {
	counts, generations atomic.Int64
	forbid              bool
	failure             error
}

func (*storageReuseProvider) Identity() string { return "offline:storage-reuse-fixture-v1" }

func (p *storageReuseProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	if p.counts.Add(1) != 1 || p.forbid {
		panic("storage reuse unexpectedly invoked provider counting")
	}
	return 10, nil
}

func (p *storageReuseProvider) Generate(_ context.Context, request inference.Request) (inference.Response, error) {
	if p.generations.Add(1) != 1 || p.forbid {
		panic("storage reuse unexpectedly invoked provider generation")
	}
	if p.failure != nil {
		return inference.Response{}, p.failure
	}
	return inference.Response{ID: "synthetic-local-answer", Model: request.Model, Text: "Synthetic checked storage answer.", Complete: true, StopReason: "stop", Usage: inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}}, nil
}

func storageReuseSpec() inference.Spec {
	return inference.Spec{Task: "storage-reuse", Revision: "approved-v1", Provider: "ollama", Model: "synthetic-local-fixture", Instructions: "Use only this synthetic fixture.", Input: "Return the synthetic storage answer.", MaxContextBytes: 1024, MaxInputTokens: 100, MaxOutputTokens: 20, ReuseForSeconds: 3600, DailyTokenBudget: &inference.TokenBudget{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}}
}

func storageInferenceRunner(f *storageReuseFixture, provider inference.Provider) inference.Runner {
	return inference.Runner{Broker: f.client, Provider: provider, Now: func() time.Time { return time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC) }}
}

func storageRevisionRequest() revision.Request {
	text := "Synthetic output is draft."
	return revision.Request{Schema: revision.Schema, Task: "storage-revision", Revision: "approved-v1", Source: revision.Source{Text: &text, SHA256: revision.Digest(text)}, Edits: []revision.Edit{{Old: "draft", New: "ready", ExpectedMatches: 1}}, SourceChecks: []evaluation.Check{{ID: "source", Kind: "contains", Text: "is draft."}}, Checks: []evaluation.Check{{ID: "result", Kind: "contains", Text: "is ready."}}}
}

func assertStorageInferenceReuse(t *testing.T, first, reused inference.Result) {
	t.Helper()
	if !reused.Reused || !reused.Durable || reused.Response.Text != first.Response.Text || reused.Fingerprint != first.Fingerprint || reused.CreatedAt != first.CreatedAt || reused.ExpiresAt != first.ExpiresAt || reused.ThisRunUsage != (inference.Usage{}) || reused.Response.Usage != (inference.Usage{}) || reused.AvoidedUsage != first.GenerationUsage || reused.Measurement == nil || reused.Measurement.GenerationCalled || reused.CostUSD != nil && *reused.CostUSD != 0 {
		t.Fatalf("archived result lost exact zero-generation reuse: first=%+v reused=%+v", first, reused)
	}
}

func assertStorageRevisionReuse(t *testing.T, first, reused revision.Result) {
	t.Helper()
	if !reused.Reused || !reused.Durable || reused.Status != "completed" || reused.Text != first.Text || reused.OutputSHA256 != first.OutputSHA256 || reused.SourceSHA256 != first.SourceSHA256 || reused.RequestSHA256 != first.RequestSHA256 || reused.Artifact == nil || first.Artifact == nil || *reused.Artifact != *first.Artifact || reused.GenerationCalled || reused.ThisRunUsage != (inference.Usage{}) || reused.ProviderCostUSD != 0 || reused.CostBasis != "reuse_zero" {
		t.Fatalf("archived revision lost immutable zero-generation reuse: first=%+v reused=%+v", first, reused)
	}
}

func TestStorageArchivedRunnerResultsReuseAfterBrokerRestart(t *testing.T) {
	f := newStorageReuseFixture(t)
	provider := &storageReuseProvider{}
	spec := storageReuseSpec()
	first, err := storageInferenceRunner(f, provider).Run(t.Context(), spec)
	if err != nil || !first.Durable || first.Reused || provider.generations.Load() != 1 {
		t.Fatal("fresh synthetic inference failed", first, err)
	}
	request := storageRevisionRequest()
	firstRevision, err := (revision.Runner{Broker: f.client}).Run(t.Context(), request)
	if err != nil || !firstRevision.Durable || firstRevision.Reused || firstRevision.Artifact == nil {
		t.Fatal("fresh checked revision failed", firstRevision, err)
	}
	ledgerKey := "inference/v1/token-budget/2030-01-02"
	ledgerBefore, err := f.client.Get(t.Context(), ledgerKey)
	if err != nil {
		t.Fatal(err)
	}
	if report := f.archive(t); report.Items < 2 || !f.currentIsCold(t, "inference/v1/jobs/"+first.Fingerprint) || !f.currentIsCold(t, firstRevision.Artifact.Key) || f.currentIsCold(t, ledgerKey) {
		t.Fatal("completed current results were not cold or current quota was archived", report)
	}
	f.restart(t)
	reused, err := storageInferenceRunner(f, provider).Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	assertStorageInferenceReuse(t, first, reused)
	reusedRevision, err := (revision.Runner{Broker: f.client}).Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertStorageRevisionReuse(t, firstRevision, reusedRevision)
	ledgerAfter, err := f.client.Get(t.Context(), ledgerKey)
	if err != nil || ledgerAfter != ledgerBefore || provider.counts.Load() != 1 || provider.generations.Load() != 1 {
		t.Fatal("cold reuse touched quota or provider", ledgerAfter, err)
	}
}

// Both exact publication retries commit through the HTTP broker, while their
// acknowledgements are lost. The real runner must retain its pending claim.
type storageLostClaimAcknowledgement struct {
	inference.Broker
	key string
}

func (b *storageLostClaimAcknowledgement) Publish(ctx context.Context, request protocol.PublishRequest) (protocol.PublishResult, error) {
	b.key = request.Key
	_, err := b.Broker.Publish(ctx, request)
	if err != nil {
		return protocol.PublishResult{}, err
	}
	return protocol.PublishResult{}, errors.New("synthetic lost claim acknowledgement")
}

func TestStorageArchivePreservesRealPendingAndUncertainClaimsAcrossRestart(t *testing.T) {
	for _, state := range []string{"pending", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			f := newStorageReuseFixture(t)
			for i, content := range []string{"synthetic old reference", "synthetic current reference"} {
				if _, err := f.client.Publish(t.Context(), protocol.PublishRequest{Key: "reference", OperationID: content, ExpectedVersion: int64(i), Content: content}); err != nil {
					t.Fatal(err)
				}
			}
			provider := &storageReuseProvider{forbid: state == "pending"}
			runner := storageInferenceRunner(f, provider)
			var lost *storageLostClaimAcknowledgement
			if state == "pending" {
				lost = &storageLostClaimAcknowledgement{Broker: f.client}
				runner.Broker = lost
			} else {
				provider.failure = errors.New("synthetic unknown generation outcome")
			}
			result, err := runner.Run(t.Context(), storageReuseSpec())
			if err == nil || result.Measurement == nil {
				t.Fatal("fault injection did not preserve a stopped run", result, err)
			}
			key := "inference/v1/jobs/" + result.Measurement.Fingerprint
			if lost != nil && lost.key != key {
				t.Fatal("lost acknowledgement did not concern the real inference claim")
			}
			before, err := f.client.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			var claim struct {
				State string `json:"state"`
			}
			if json.Unmarshal([]byte(before.Content), &claim) != nil || claim.State != state {
				t.Fatal("runner did not save the intended recovery state", before)
			}
			var ledgerBefore protocol.Artifact
			if state == "uncertain" {
				ledgerBefore, err = f.client.Get(t.Context(), "inference/v1/token-budget/2030-01-02")
				if err != nil {
					t.Fatal(err)
				}
				var ledger struct {
					Entries map[string]struct {
						Actual   json.RawMessage       `json:"actual"`
						Reserved inference.TokenBudget `json:"reserved"`
					} `json:"entries"`
				}
				if json.Unmarshal([]byte(ledgerBefore.Content), &ledger) != nil || len(ledger.Entries) != 1 {
					t.Fatal("unknown generation lost its quota reservation", ledgerBefore)
				}
				for _, entry := range ledger.Entries {
					if len(entry.Actual) != 0 || entry.Reserved.TotalTokens != 120 {
						t.Fatal("unknown generation was settled or its declared cap was lost", ledgerBefore)
					}
				}
			}
			if report := f.archive(t); report.Items < 1 {
				t.Fatal("claim safety was not checked during real archival", report)
			}
			if f.currentIsCold(t, key) {
				t.Fatal("active claim moved out of hot current state")
			}
			f.restart(t)
			runner = storageInferenceRunner(f, provider)
			runner.Now = func() time.Time { return time.Date(2031, 1, 2, 12, 0, 0, 0, time.UTC) }
			if retried, err := runner.Run(t.Context(), storageReuseSpec()); !errors.Is(err, inference.ErrInFlight) || retried.Response.Text != "" {
				t.Fatal("old active claim was regenerated or disclosed output", retried, err)
			}
			after, err := f.client.Get(t.Context(), key)
			if err != nil || after != before {
				t.Fatal("archival/restart changed the active claim", after, err)
			}
			if state == "uncertain" {
				ledgerAfter, err := f.client.Get(t.Context(), "inference/v1/token-budget/2030-01-02")
				if err != nil || ledgerAfter != ledgerBefore || f.currentIsCold(t, ledgerAfter.Key) || provider.counts.Load() != 1 || provider.generations.Load() != 1 {
					t.Fatal("unknown reservation was released or generation repeated", ledgerAfter, err)
				}
			} else if provider.counts.Load() != 0 || provider.generations.Load() != 0 {
				t.Fatal("uncertain claim acknowledgement reached provider")
			}
		})
	}
}

func TestStorageMissingOrCorruptCurrentResultNeverRegenerates(t *testing.T) {
	for _, failure := range []string{"missing", "corrupt"} {
		for _, kind := range []string{"inference", "revision"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				f := newStorageReuseFixture(t)
				provider := &storageReuseProvider{}
				spec, request := storageReuseSpec(), storageRevisionRequest()
				first, err := storageInferenceRunner(f, provider).Run(t.Context(), spec)
				if err != nil {
					t.Fatal(err)
				}
				firstRevision, err := (revision.Runner{Broker: f.client}).Run(t.Context(), request)
				if err != nil || firstRevision.Artifact == nil {
					t.Fatal(err)
				}
				key := "inference/v1/jobs/" + first.Fingerprint
				// Read the real head version rather than assuming how many quota
				// reservation publications this runner currently needs.
				head, err := f.client.Get(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				version := head.Version
				if kind == "revision" {
					key, version = firstRevision.Artifact.Key, firstRevision.Artifact.Version
				}
				f.archive(t)
				if !f.currentIsCold(t, key) {
					t.Fatal("test did not archive the current result")
				}
				file := f.archivedFile(t, key, version)
				original, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				wantCode := "archive_corrupt"
				if failure == "missing" {
					err, wantCode = os.Remove(file), "archive_unavailable"
				} else {
					err = os.WriteFile(file, []byte("synthetic corrupt archive"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				f.restart(t)
				if _, err := f.client.Get(t.Context(), key); err == nil {
					t.Fatal("broken archived current result was accepted")
				} else {
					var application *client.Error
					if !errors.As(err, &application) || application.Code != wantCode || application.Code == "not_found" {
						t.Fatal("cold error could be mistaken for a new task", err)
					}
				}
				if kind == "inference" {
					stopped, err := storageInferenceRunner(f, provider).Run(t.Context(), spec)
					if err == nil || stopped.Reused || stopped.Durable || stopped.Response.Text != "" || stopped.Measurement == nil || stopped.Measurement.GenerationCalled {
						t.Fatal("inference hid broken history or generated again", stopped, err)
					}
				} else {
					stopped, err := (revision.Runner{Broker: f.client}).Run(t.Context(), request)
					if !errors.Is(err, revision.ErrPublication) || stopped.Text != "" || stopped.Durable || stopped.GenerationCalled || stopped.ThisRunUsage != (inference.Usage{}) || stopped.ProviderCostUSD != 0 {
						t.Fatal("revision disclosed an unconfirmed result", stopped, err)
					}
				}
				if !f.currentIsCold(t, key) || f.archivedFile(t, key, version) != file || provider.counts.Load() != 1 || provider.generations.Load() != 1 {
					t.Fatal("failed archived read changed durable identity or invoked provider")
				}
				if err := os.WriteFile(file, original, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "inference" {
					recovered, err := storageInferenceRunner(f, provider).Run(t.Context(), spec)
					if err != nil {
						t.Fatal(err)
					}
					assertStorageInferenceReuse(t, first, recovered)
				} else {
					recovered, err := (revision.Runner{Broker: f.client}).Run(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
					assertStorageRevisionReuse(t, firstRevision, recovered)
				}
			})
		}
	}
}

func TestStorageTopLevelCommandRoutesWithoutCreatingState(t *testing.T) {
	if err := run([]string{"storage", "--help"}); err != nil {
		t.Fatal("top-level storage help is not routed", err)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if err := run([]string{"storage", "info", "--db", missing}); err == nil || !strings.Contains(err.Error(), "existing regular broker database") {
		t.Fatal("top-level storage command did not enforce existing state", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("top-level storage inspection created a database", err)
	}
}
