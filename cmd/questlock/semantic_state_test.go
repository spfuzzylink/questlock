package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
)

func semanticPrivateTestParent(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func semanticStateModelFixture(t *testing.T, unknown bool) (string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	metadata, generations := &atomic.Int64{}, &atomic.Int64{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			metadata.Add(1)
			io.WriteString(w, `{"version":"fixture-1"}`)
		case "/api/tags":
			metadata.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture:embed", "digest": strings.Repeat("a", 64)}, map[string]any{"name": "fixture:verify", "digest": strings.Repeat("b", 64)}}})
		case "/api/show":
			metadata.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"template": "{{ .System }} {{ .Prompt }}", "capabilities": []string{"embedding", "completion"}, "model_info": map[string]any{"general.architecture": "fixture", "fixture.context_length": 65536}})
		case "/api/embed":
			io.WriteString(w, `{"model":"fixture:embed","embeddings":[[1,0]],"prompt_eval_count":3}`)
		case "/api/generate":
			generations.Add(1)
			if unknown {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, `{"error":"synthetic uncertain outcome"}`)
				return
			}
			var body struct {
				Prompt string `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			var fields map[string]json.RawMessage
			json.Unmarshal([]byte(strings.TrimPrefix(body.Prompt, "\n\nTask:\n")), &fields)
			text := `{"verdict":"accept","supported":true,"complete":true}`
			if fields["question"] != nil {
				text = `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "fixture:verify", "response": text, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 20})
		default:
			t.Error("unexpected fixture endpoint", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s.URL, metadata, generations
}

func semanticStateCommandArgs(t *testing.T, parent, endpoint string) []string {
	t.Helper()
	suite := semanticEvalTestSuite()
	suite.Cases[0].Variants = suite.Cases[0].Variants[:1]
	path := filepath.Join(parent, "suite.json")
	if err := os.WriteFile(path, semanticEvalTestJSON(t, suite), 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"--live", "--suite", path, "--ollama-url", endpoint, "--embedding-model", "fixture:embed", "--verifier-model", "fixture:verify", "--verifier-policy", "factorized-v1"}
}

func TestSemanticEvaluationRetainsExplicitStateAndFailedTemporaryState(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			t.Run(map[bool]string{true: "explicit", false: "temporary"}[explicit]+"/"+map[bool]string{true: "unknown", false: "known"}[unknown], func(t *testing.T) {
				parent := semanticPrivateTestParent(t)
				t.Setenv("TMPDIR", parent)
				endpoint, _, calls := semanticStateModelFixture(t, unknown)
				args := semanticStateCommandArgs(t, parent, endpoint)
				requested := filepath.Join(parent, "explicit-state")
				if explicit {
					args = append(args, "--state-dir", requested)
				}
				var out, diagnostics bytes.Buffer
				err := semanticEvaluateCommand(args, &out, &diagnostics)
				if (err != nil) != unknown || calls.Load() != map[bool]int64{true: 1, false: 2}[unknown] {
					t.Fatalf("evaluation did not preserve expected single-attempt behavior: err=%v calls=%d", err, calls.Load())
				}
				var report semanticEvalReport
				if json.Unmarshal(out.Bytes(), &report) != nil || report.StateRetained != (explicit || unknown) {
					t.Fatal("report lost state-retention status")
				}
				if bytes.Contains(out.Bytes(), []byte(parent)) || bytes.Contains(out.Bytes(), []byte("worker.token")) {
					t.Fatal("public JSON report exposed private state location or credential")
				}
				var dirs []string
				if explicit {
					dirs = []string{requested}
				} else {
					dirs, err = filepath.Glob(filepath.Join(parent, "questlock-semantic-evaluation-*"))
					if err != nil {
						t.Fatal(err)
					}
				}
				if !explicit && !unknown {
					if len(dirs) != 0 || diagnostics.Len() != 0 {
						t.Fatal("fully known successful temporary state was not disposed")
					}
					return
				}
				if len(dirs) != 1 || !strings.Contains(diagnostics.String(), dirs[0]) {
					t.Fatal("retained state is not discoverable in private diagnostics")
				}
				for name, want := range map[string]os.FileMode{"": 0700, "state.db": 0600, "worker.token": 0600} {
					info, err := os.Stat(filepath.Join(dirs[0], name))
					if err != nil || info.Mode().Perm() != want {
						t.Fatalf("state permission mismatch for %q: %v", name, err)
					}
				}
				stored, err := store.Open(filepath.Join(dirs[0], "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer stored.Close()
				token, err := os.ReadFile(filepath.Join(dirs[0], "worker.token"))
				if err != nil {
					t.Fatal(err)
				}
				principal, err := stored.Authenticate(t.Context(), strings.TrimSpace(string(token)))
				if err != nil || principal.Scope != "semantic-evaluation" {
					t.Fatal("persisted scoped token cannot inspect retained state")
				}
				if unknown {
					work := report.Pairs[0].Verification.Work[0]
					artifact, err := stored.Get(t.Context(), principal, work.ReceiptKey)
					var job struct {
						State string `json:"state"`
					}
					if err != nil || json.Unmarshal([]byte(artifact.Content), &job) != nil || job.State != "uncertain" && job.State != "pending" {
						t.Fatalf("uncertain source claim was erased: %v", err)
					}
				}
			})
		}
	}
}

type semanticUncertainProvider struct{ calls int }

func (*semanticUncertainProvider) Identity() string { return "semantic-retention-offline" }
func (*semanticUncertainProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 100, nil
}
func (p *semanticUncertainProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.calls++
	return inference.Response{}, errors.New("offline uncertain generation")
}

func TestSemanticEvaluationRetainedClaimAndReservationFenceAnotherGeneration(t *testing.T) {
	parent := semanticPrivateTestParent(t)
	t.Setenv("TMPDIR", parent)
	state, err := newSemanticEvalBroker(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	provider := &semanticUncertainProvider{}
	runner := &inference.Runner{Broker: state.broker, Provider: provider}
	spec := inference.Spec{Task: "retained-evaluation", Revision: "approved-1", Provider: "ollama", Model: "fixture:local", Instructions: "Use only approved evidence.", Input: "Explain only this record.", MaxInputTokens: 100, MaxOutputTokens: 32, ReuseForSeconds: 60, DailyTokenBudget: &inference.TokenBudget{InputTokens: 100, OutputTokens: 32, TotalTokens: 132}}
	first, err := runner.Run(t.Context(), spec)
	if err == nil || provider.calls != 1 || first.Measurement == nil || !first.Measurement.GenerationCalled || first.Measurement.KnownGenerationUsage {
		t.Fatalf("fixture did not create exactly one uncertain durable invocation: err=%v calls=%d measurement=%+v", err, provider.calls, first.Measurement)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Open(filepath.Join(state.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	token, err := os.ReadFile(filepath.Join(state.dir, "worker.token"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(stored))
	defer server.Close()
	broker, err := client.New(server.URL, strings.TrimSpace(string(token)))
	if err != nil {
		t.Fatal(err)
	}
	runner.Broker = broker
	if _, _, err := runner.Lookup(t.Context(), spec); !errors.Is(err, inference.ErrInFlight) {
		t.Fatalf("closed/reopened unknown claim no longer fences lookup: %v", err)
	}
	if _, err := runner.Run(t.Context(), spec); !errors.Is(err, inference.ErrInFlight) || provider.calls != 1 {
		t.Fatalf("unknown diagnostic outcome silently regenerated: %v calls=%d", err, provider.calls)
	}
	spec.Revision = "distinct-budget-probe"
	if _, err := runner.Run(t.Context(), spec); !errors.Is(err, inference.ErrBudgetExceeded) || provider.calls != 1 {
		t.Fatalf("unknown token reservation was released: %v calls=%d", err, provider.calls)
	}
}

func TestSemanticEvaluationInvalidStatePathsStopBeforeProviderMetadata(t *testing.T) {
	parent := semanticPrivateTestParent(t)
	endpoint, metadata, calls := semanticStateModelFixture(t, false)
	args := semanticStateCommandArgs(t, parent, endpoint)
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(parent, "unsafe")
	if err := os.Mkdir(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	unsafeChild := filepath.Join(unsafe, "owned-child")
	if err := os.Mkdir(unsafeChild, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative-state", existing, link, filepath.Join(link, "nested"), filepath.Join(unsafe, "new"), filepath.Join(unsafeChild, "new"), filepath.Join(parent, "missing", "new")} {
		var out, diagnostics bytes.Buffer
		if err := semanticEvaluateCommand(append(append([]string{}, args...), "--state-dir", path), &out, &diagnostics); err == nil || metadata.Load() != 0 || calls.Load() != 0 {
			t.Fatal("invalid state path reached metadata, broker reuse or inference")
		}
		var report semanticEvalReport
		if json.Unmarshal(out.Bytes(), &report) != nil || report.StateRetained || len(report.Errors) != 1 || report.Errors[0] != "evaluation_state_setup_failed" || bytes.Contains(out.Bytes(), []byte(path)) {
			t.Fatal("state preflight leaked paths or returned a misleading status")
		}
	}
}

type semanticStateFailWriter struct{}

func (semanticStateFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("offline report delivery failure")
}

func TestSemanticEvaluationReportDeliveryFailureDoesNotDiscardState(t *testing.T) {
	parent := semanticPrivateTestParent(t)
	t.Setenv("TMPDIR", parent)
	endpoint, _, calls := semanticStateModelFixture(t, false)
	args := semanticStateCommandArgs(t, parent, endpoint)
	var diagnostics bytes.Buffer
	if err := semanticEvaluateCommand(args, semanticStateFailWriter{}, &diagnostics); err == nil || calls.Load() != 2 {
		t.Fatal("report-delivery failure did not retain its completed attempt")
	}
	dirs, err := filepath.Glob(filepath.Join(parent, "questlock-semantic-evaluation-*"))
	if err != nil || len(dirs) != 1 || !strings.Contains(diagnostics.String(), dirs[0]) {
		t.Fatal("report failure lost the independently inspectable state")
	}
	stored, err := store.Open(filepath.Join(dirs[0], "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	stored.Close()
}
