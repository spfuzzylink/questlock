// Package teamquest demonstrates the real broker/claim/check/reuse path with
// scripted providers. It never calls a model and is not an inference benchmark.
package teamquest

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
	"github.com/spfuzzylink/questlock/team"
)

type Row struct {
	ID           string `json:"id"`
	Complexity   string `json:"complexity"`
	Passed       bool   `json:"passed"`
	FixtureCalls int64  `json:"scripted_provider_calls"`
	ReuseCalls   int64  `json:"reuse_scripted_provider_calls"`
}
type Report struct {
	Schema           int    `json:"schema"`
	Mode             string `json:"mode"`
	PhysicalHosts    int    `json:"physical_hosts"`
	WorkerIdentities int    `json:"worker_identities"`
	ModelCalls       int    `json:"actual_model_calls"`
	Rows             []Row  `json:"cases"`
	Passed           bool   `json:"passed"`
}

func Run(ctx context.Context) (Report, error) {
	out := Report{Schema: 1, Mode: "scripted_providers_real_https_broker_not_inference_benchmark", PhysicalHosts: 1, WorkerIdentities: 3}
	dir, err := os.MkdirTemp("", "questlock-team-quest-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		return out, err
	}
	defer s.Close()
	tokens := make([]string, 3)
	for i, id := range []string{"node-a", "node-b", "node-c"} {
		tokens[i], err = s.CreateToken(ctx, id, "team-quest")
		if err != nil {
			return out, err
		}
	}
	server := httptest.NewTLSServer(httpapi.New(s))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	peers := make([]*client.Client, 3)
	for i := range peers {
		peers[i], err = client.NewWithTLSRoots(server.URL, tokens[i], ca)
		if err != nil {
			return out, err
		}
	}
	master := team.Role{ID: "master", Driver: "ollama", Model: "scripted:fixture", ModelDigest: strings.Repeat("a", 64), Instructions: "Return only the requested JSON.", MaxInputTokens: 4096, MaxOutputTokens: 256}
	a, b := master, master
	a.ID = "review-correctness"
	b.ID = "review-risk"
	template := team.Request{Schema: 1, Master: master, Reviewers: []team.Role{a, b}, ReuseForSeconds: 3600}
	for _, c := range team.DefaultSuite(template).Cases {
		calls := new(atomic.Int64)
		engines := make([]team.Engine, 3)
		for i, peer := range peers {
			engines[i] = team.Engine{Broker: peer, WorkerID: []string{"node-a", "node-b", "node-c"}[i], Factory: func(_ context.Context, r team.Role) (delegation.Runner, error) {
				answer := string(c.Request.Checks[0].Expected)
				if r.ID != "master" {
					answer = `{"approved":true,"issues":[]}`
				}
				return inference.Runner{Broker: peer, Provider: fixtureProvider{role: r.ID, answer: answer, calls: calls}}, nil
			}}
		}
		early, err := engines[2].Run(ctx, c.Request, "review-risk")
		if err != nil || early.Status != "waiting" || calls.Load() != 0 {
			return out, errors.New("early reviewer claimed unfinished work")
		}
		var result team.Report
		for i, role := range []string{"master", "review-correctness", "review-risk"} {
			result, err = engines[i].Run(ctx, c.Request, role)
			if err != nil {
				return out, err
			}
		}
		before := calls.Load()
		engines[0].Factory = func(context.Context, team.Role) (delegation.Runner, error) {
			return nil, errors.New("reuse initialized a provider")
		}
		reused, err := engines[0].Run(ctx, c.Request, "all")
		pass := err == nil && result.Status == "completed" && result.AccountingComplete && len(result.Steps) == 3 && result.Steps[0].WorkerID == "node-a" && result.Steps[1].WorkerID == "node-b" && result.Steps[2].WorkerID == "node-c" && reused.Text == result.Text && reused.LocalUsage.GenerationCalls == 0 && before == 3 && calls.Load() == before
		out.Rows = append(out.Rows, Row{ID: c.ID, Complexity: c.Complexity, Passed: pass, FixtureCalls: before, ReuseCalls: calls.Load() - before})
		if !pass {
			return out, errors.New("team quest failed")
		}
	}
	out.Passed = true
	return out, nil
}

type fixtureProvider struct {
	role, answer string
	calls        *atomic.Int64
}

func (p fixtureProvider) Identity() string { return "team-quest-scripted/" + p.role }
func (p fixtureProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 100, nil
}
func (p fixtureProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.calls.Add(1)
	return inference.Response{ID: "scripted", Model: "scripted:fixture", Text: p.answer, Complete: true, StopReason: "stop", Usage: inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}}, nil
}
