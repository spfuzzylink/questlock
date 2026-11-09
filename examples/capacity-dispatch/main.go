// A model-free demonstration of waiting/dispatch and shared exact results.
package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/client"
	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/execution"
	"github.com/spfuzzylink/questlock/inference"
	"github.com/spfuzzylink/questlock/internal/httpapi"
	"github.com/spfuzzylink/questlock/internal/store"
)

type scriptedProvider struct{ calls atomic.Int32 }

func (*scriptedProvider) Identity() string { return "capacity-demo-scripted-runtime-v1" }
func (*scriptedProvider) SelfHosted() bool { return true }
func (*scriptedProvider) CountInputTokens(context.Context, inference.Request) (int64, error) {
	return 10, nil
}
func (p *scriptedProvider) Generate(context.Context, inference.Request) (inference.Response, error) {
	p.calls.Add(1)
	return inference.Response{Model: "scripted", Text: `{"state":"ready"}`, Complete: true,
		Usage: inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 6}}, nil
}

func capacity(pressure admission.Pressure) inference.AdmissionFunc {
	gate, err := delegation.NewResourceAdmission(delegation.ReuseAdmissionPolicy{}, admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		return admission.Snapshot{At: time.Now(), Known: true, CPUCapacityMilli: 1000, LoadCapacityMilli: 1000,
			AvailableMemoryBytes: 2 << 30, MemoryPressure: pressure}, nil
	}))
	if err != nil {
		panic(err) // Fixed example configuration, never task/model output.
	}
	return gate
}

func demo(ctx context.Context) (report map[string]any, runErr error) {
	root, err := os.MkdirTemp("", "questlock-capacity-demo-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if runErr == nil {
			_ = os.RemoveAll(root)
		} else {
			fmt.Fprintln(os.Stderr, "Inspect the retained demonstration state:", root)
		}
	}()
	s, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	server := httptest.NewTLSServer(httpapi.New(s))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	connect := func(agent string) (*client.Client, error) {
		token, err := s.CreateToken(ctx, agent, "capacity-demo")
		if err != nil {
			return nil, err
		}
		return client.NewWithTLSRoots(server.URL, token, ca)
	}
	a, err := connect("worker-a")
	if err != nil {
		return nil, err
	}
	b, err := connect("worker-b")
	if err != nil {
		return nil, err
	}
	busy, ready, repeat := &scriptedProvider{}, &scriptedProvider{}, &scriptedProvider{}
	d, err := execution.NewDispatcher(a, []execution.Node{
		{ID: "busy-node", Provider: busy, Admission: capacity(admission.Critical)},
		{ID: "ready-node", Provider: ready, Admission: capacity(admission.Normal)},
	}, execution.DispatchOptions{})
	if err != nil {
		return nil, err
	}
	spec := inference.Spec{Task: "capacity-demo/check-state", Revision: "approved-1", Provider: "ollama", Model: "scripted",
		Instructions: "Return the approved state as JSON.", Input: "The state is ready.", MaxInputTokens: 4096,
		MaxOutputTokens: 64, ReuseForSeconds: 3600}
	first, err := d.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	// Independent client/provider object, same scope and immutable contract.
	other, err := execution.NewDispatcher(b, []execution.Node{
		{ID: "busy-repeat-node", Provider: repeat, Admission: capacity(admission.Critical)},
	}, execution.DispatchOptions{})
	if err != nil {
		return nil, err
	}
	exact, err := other.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	if first.SelectedNode != "ready-node" || first.Response.Text != `{"state":"ready"}` || !first.Durable ||
		!exact.Reused || !exact.Durable || exact.Response.Text != first.Response.Text || busy.calls.Load() != 0 ||
		ready.calls.Load() != 1 || repeat.calls.Load() != 0 || exact.ThisRunUsage != (inference.Usage{}) {
		return nil, errors.New("scripted capacity/reuse checks failed")
	}
	return map[string]any{"kind": "scripted_two_clients_one_physical_host", "real_model_calls": 0,
		"paid_fallback_enabled": false, "provider_api_charge_usd": 0, "first_node": first.SelectedNode,
		"first_node_stats": first.Nodes, "exact_reused": exact.Reused, "exact_new_input": exact.ThisRunUsage.InputTokens,
		"exact_new_output": exact.ThisRunUsage.OutputTokens, "scripted_generations": ready.calls.Load()}, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := demo(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
