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
	"testing"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/evaluation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/protocol"
)

func TestFrozenSixCasesHaveExactFormsAndFactChecks(t *testing.T) {
	set, err := loadCases()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"support": true, "onboarding": true, "incident": true, "release": true, "refund": true, "broker": true}
	for _, c := range set.Cases {
		if !want[c.ID] {
			t.Fatalf("unexpected business case %q", c.ID)
		}
		delete(want, c.ID)
		var expected map[string]any
		if json.Unmarshal(c.Checks[1].Expected, &expected) != nil || len(expected) != len(c.OutputTypes) {
			t.Fatalf("case %s needs one checked primitive per declared field", c.ID)
		}
		if _, passed := evaluation.CheckOutput(string(c.Checks[1].Expected), c.Checks); !passed {
			t.Fatal("reference facts do not pass their declared checks")
		}
		for field, typ := range c.OutputTypes {
			v, ok := expected[field]
			if !ok {
				t.Fatalf("unverified field %s/%s", c.ID, field)
			}
			switch typ {
			case "boolean":
				if _, ok := v.(bool); !ok {
					t.Fatal("non-Boolean expected value")
				}
			case "integer":
				n, ok := v.(float64)
				if !ok || n != float64(int64(n)) {
					t.Fatal("noninteger expected quantity")
				}
			case "string":
				if _, ok := v.(string); !ok {
					t.Fatal("non-string expected field")
				}
			default:
				t.Fatal("unsupported primitive in public example")
			}
			changed := map[string]any{}
			for key, value := range expected {
				changed[key] = value
			}
			switch value := v.(type) {
			case bool:
				changed[field] = !value
			case float64:
				changed[field] = value + 1
			case string:
				changed[field] = value + "-wrong"
			}
			data, _ := json.Marshal(changed)
			if _, passed := evaluation.CheckOutput(string(data), c.Checks); passed {
				t.Fatalf("wrong business fact %s/%s passed", c.ID, field)
			}
		}
		base := requestFor(set.Revision, c, nil)
		var canonical []byte
		for _, form := range c.Approved.Forms {
			raw := base
			raw.Spec.Input = form
			prepared, decision, err := delegation.ApplyApprovedQuestions(raw, c.Approved)
			if err != nil || !decision.Matched || delegation.Validate(prepared, false) != nil || prepared.SemanticReuse != nil || prepared.SemanticShadow != nil || prepared.Policy.Escalation != nil || prepared.TrackOutcome {
				t.Fatalf("invalid ordinary prepared request: %+v %v", decision, err)
			}
			encoded, _ := json.Marshal(prepared)
			if canonical == nil {
				canonical = encoded
			} else if !bytes.Equal(canonical, encoded) {
				t.Fatal("declared forms did not prepare identical ordinary requests")
			}
			prompt, err := inference.BuildPlan(inference.Spec{Task: prepared.Spec.Task, Revision: prepared.Spec.Revision, Provider: "ollama", Model: "qwen3.5:4b", Instructions: prepared.Spec.Instructions, Input: prepared.Spec.Input, Context: prepared.Spec.Context, MaxContextBytes: 16 << 10, MaxInputTokens: 8192, MaxOutputTokens: 256})
			if err != nil || strings.Contains(prompt.Request.Prefix+prompt.Request.Input, string(c.Checks[1].Expected)) {
				t.Fatal("expected-check JSON was inserted into the model prompt")
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing business cases: %v", want)
	}
}

func TestDefaultAndInvalidLiveFlagsMakeNoRequestsOrFiles(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	state, output := filepath.Join(dir, "state.jsonl"), filepath.Join(dir, "output.jsonl")
	args := []string{"--broker", server.URL, "--ollama", server.URL, "--token-file", filepath.Join(dir, "absent.token"), "--state", state, "--output", output, "--fresh-baseline"}
	var out bytes.Buffer
	if err := run(args, &out); err != nil || !strings.Contains(out.String(), "No model or file access") || calls != 0 {
		t.Fatalf("default tried live work: %v %d", err, calls)
	}
	for _, path := range []string{state, output} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("default created a file")
		}
	}
	if err := run([]string{"--live", "--broker", "http://localhost:8080", "--ollama", server.URL}, io.Discard); err == nil || calls != 0 {
		t.Fatal("invalid loopback origin reached a provider")
	}
	if err := run([]string{"--live", "--broker", server.URL, "--ollama", server.URL, "--max-total-tokens", "50689"}, io.Discard); err == nil || calls != 0 {
		t.Fatal("budget increase reached a provider")
	}
}

func TestPrivateOutputsRefuseExistingFilesBeforeProviderAccess(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	for _, existing := range []string{"state", "output"} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		token := filepath.Join(dir, "scoped.token")
		state := filepath.Join(dir, "state")
		output := filepath.Join(dir, "output")
		if err := os.WriteFile(token, []byte("fixture-scoped-token"), 0600); err != nil {
			t.Fatal(err)
		}
		path := state
		if existing == "output" {
			path = output
		}
		if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		err := run([]string{"--live", "--broker", server.URL, "--ollama", server.URL, "--token-file", token, "--state", state, "--output", output}, io.Discard)
		data, _ := os.ReadFile(path)
		if err == nil || calls != 0 || string(data) != "retained" {
			t.Fatal("existing receipt overwritten or provider contacted")
		}
	}
	for _, raw := range []string{"", "http://localhost:11434", "http://example.invalid:80", "http://127.0.0.1:11434/path", "http://a@127.0.0.1:11434", "http://127.0.0.1:11434?q=x", "http://127.0.0.1:11434#x"} {
		if loopbackOrigin(raw) {
			t.Fatalf("accepted non-origin %q", raw)
		}
	}
}

type fixtureBroker struct {
	db        *store.Store
	principal store.Principal
}

func (b fixtureBroker) Get(ctx context.Context, key string) (protocol.Artifact, error) {
	a, err := b.db.Get(ctx, b.principal, key)
	return a, asClientError(err)
}
func (b fixtureBroker) Publish(ctx context.Context, r protocol.PublishRequest) (protocol.PublishResult, error) {
	v, err := b.db.Publish(ctx, b.principal, r)
	return v, asClientError(err)
}
func asClientError(err error) error {
	var e *store.Error
	if errors.As(err, &e) {
		return &client.Error{Code: e.Code, Message: e.Message, CurrentVersion: e.CurrentVersion}
	}
	return err
}

type measuredFixture struct {
	cases               caseSet
	mode                string
	generations, counts int
}

func (p *measuredFixture) Identity() string { return "approved-question-offline-fixture" }
func (p *measuredFixture) CountInputTokens(context.Context, inference.Request) (int64, error) {
	p.counts++
	return 120, nil
}
func (p *measuredFixture) Generate(_ context.Context, r inference.Request) (inference.Response, error) {
	p.generations++
	if p.mode == "unknown" && p.generations == 1 {
		return inference.Response{}, errors.New("fixture lost generation outcome")
	}
	for _, c := range p.cases.Cases {
		if strings.Contains(r.Prefix, c.Context[0].Text) {
			text := string(c.Checks[1].Expected)
			if p.mode == "quality" && p.generations == 1 {
				text = strings.Replace(text, `"already_completed": false`, `"already_completed": true`, 1)
			}
			return inference.Response{Model: r.Model, Text: text, Complete: true, Usage: inference.Usage{InputTokens: 120, UncachedInputTokens: 120, OutputTokens: 40}}, nil
		}
	}
	return inference.Response{}, errors.New("unknown fixture context")
}

func TestMeasurementAccountsSeedsReplaysAndOptionalFreshBaselines(t *testing.T) {
	for _, mode := range []string{"no-baseline", "baseline", "quality", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			set, err := loadCases()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(dir, "broker.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			token, err := db.CreateToken(t.Context(), "approved-question-fixture", "worker")
			if err != nil {
				t.Fatal(err)
			}
			principal, err := db.Authenticate(t.Context(), token)
			if err != nil {
				t.Fatal(err)
			}
			broker := fixtureBroker{db, principal}
			provider := &measuredFixture{cases: set, mode: mode}
			runner := &inference.Runner{Broker: broker, Provider: provider}
			router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) { return runner, nil }}
			state, err := privateFile(filepath.Join(dir, "state.jsonl"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			report, err := privateFile(filepath.Join(dir, "report.jsonl"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer report.Close()
			budget := &inference.TokenBudget{InputTokens: 147456, OutputTokens: 4608, TotalTokens: 152064}
			err = measure(t.Context(), router, broker, set, budget, mode != "no-baseline", state, report)
			if (err != nil) != (mode == "quality" || mode == "unknown") {
				t.Fatalf("unexpected measurement outcome %v", err)
			}
			wantGen := 18
			if mode == "no-baseline" {
				wantGen = 6
			}
			if mode == "unknown" {
				wantGen = 1
			}
			if provider.generations != wantGen || provider.counts != wantGen {
				t.Fatalf("wrong measured work: %d gen %d count", provider.generations, provider.counts)
			}
			data, err := os.ReadFile(report.Name())
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			rows := []reportRow{}
			var summary struct {
				Phase  string                 `json:"phase"`
				Totals map[string]phaseTotals `json:"totals"`
			}
			for decoder.More() {
				var raw json.RawMessage
				if decoder.Decode(&raw) != nil {
					t.Fatal("invalid report JSONL")
				}
				var marker struct {
					Phase string `json:"phase"`
				}
				json.Unmarshal(raw, &marker)
				if marker.Phase == "summary" {
					if json.Unmarshal(raw, &summary) != nil {
						t.Fatal("invalid summary")
					}
					continue
				}
				var row reportRow
				if json.Unmarshal(raw, &row) != nil {
					t.Fatal("invalid row")
				}
				rows = append(rows, row)
			}
			if summary.Phase != "summary" {
				t.Fatal("lost partial-run summary")
			}
			seeds, replays, baselines := summary.Totals["seed"], summary.Totals["replay"], summary.Totals["baseline"]
			if mode == "unknown" {
				if !seeds.UsageUnknown || seeds.Calls != 1 || replays.Calls != 0 || baselines.Calls != 0 {
					t.Fatal("unknown generation did not stop whole run")
				}
				return
			}
			if seeds.Generated != 6 || seeds.InputTokens != 720 || seeds.OutputTokens != 240 || replays.Generated != 0 || replays.InputTokens != 0 || replays.OutputTokens != 0 {
				t.Fatalf("lost or double-counted new work: %+v", summary.Totals)
			}
			wantReplays := 12
			if mode == "quality" {
				wantReplays = 10
				if replays.Withheld != 2 || rows[0].Result.Status != "quality_failed" || !strings.Contains(rows[0].ModelOutput, `"already_completed": true`) {
					t.Fatal("failed source, output or withheld replays omitted")
				}
			}
			if replays.Calls != wantReplays {
				t.Fatal("wrong replay count")
			}
			if mode != "no-baseline" && (baselines.Generated != 12 || baselines.InputTokens != 1440 || baselines.OutputTokens != 480) {
				t.Fatal("fresh baselines omitted or historical usage counted")
			}
			for _, row := range rows {
				if row.Phase == "replay" && row.Withheld == "" {
					if row.Result == nil || row.Result.Attempts[0].Result.ThisRunUsage != (inference.Usage{}) || row.Result.Attempts[0].Result.Measurement.GenerationCalled {
						t.Fatal("replay incurred hidden generation")
					}
				}
			}
		})
	}
}
