package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/inference"
)

// Node is an explicit trusted, self-hosted provider and its capacity gate. A
// node may wrap a caller-owned transport; Dispatcher creates no RPC service.
// Admission must honor cancellation and bound its own resource probes/queue.
// A granted lease stays held until its release callback runs, even after the
// admission context expires; admitted generation uses the original caller ctx.
// Providers, admission callbacks and the shared broker must be concurrency safe.
type Node struct {
	ID        string
	Provider  inference.Provider
	Admission inference.AdmissionFunc
}

// DispatchOptions bounds one process's calls, not machine-wide reservations.
// Zero MaxPending and PollInterval select 8 calls and 250ms. Zero MaxWait makes
// one pass, using each admission callback's own policy and the caller deadline.
// Positive MaxWait bounds capacity/peer waiting, never admitted generation.
// Dependencies must honor contexts; these limits cannot stop a blocking callback.
type DispatchOptions struct {
	MaxPending   int
	MaxWait      time.Duration
	PollInterval time.Duration
	WaitForPeer  bool
}

// NodeStats aggregates attempts without retaining an unbounded history or text.
type NodeStats struct {
	ID              string  `json:"id"`
	Runs            int     `json:"runs"`
	Deferrals       int     `json:"deferrals"`
	PeerPolls       int     `json:"peer_polls"`
	GenerationCalls int     `json:"generation_calls"`
	AdmissionWaitMS float64 `json:"admission_wait_ms"`
}

type DispatchResult struct {
	inference.Result
	SelectedNode string      `json:"selected_node,omitempty"`
	Nodes        []NodeStats `json:"nodes"`
}

// Dispatcher visits explicit nodes only after proven preclaim capacity deferral.
// Every runner uses the same broker and captured runtime identity, preserving
// existing task/provider keys and ownership fences. This is not a cluster
// scheduler, a quality policy, a billing audit or automatic provider fallback.
type Dispatcher struct {
	nodes   []Node
	broker  inference.Broker
	runtime string
	options DispatchOptions
	slots   chan struct{}
}

func NewDispatcher(broker inference.Broker, nodes []Node, opts DispatchOptions) (*Dispatcher, error) {
	if opts.MaxPending == 0 {
		opts.MaxPending = 8
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 250 * time.Millisecond
	}
	if nilInterface(broker) || len(nodes) == 0 || len(nodes) > 16 || opts.MaxPending < 1 || opts.MaxPending > 64 || opts.MaxWait < 0 || opts.MaxWait > 24*time.Hour || opts.PollInterval < 10*time.Millisecond || opts.PollInterval > time.Minute {
		return nil, ErrConfiguration
	}
	d := &Dispatcher{broker: broker, nodes: append([]Node(nil), nodes...), options: opts, slots: make(chan struct{}, opts.MaxPending)}
	seen := map[string]bool{}
	for _, node := range d.nodes {
		if node.ID == "" || len(node.ID) > 128 || !utf8.ValidString(node.ID) || seen[node.ID] || nilInterface(node.Provider) || node.Admission == nil {
			return nil, ErrConfiguration
		}
		for _, r := range node.ID {
			if r < 32 || r == 127 {
				return nil, ErrConfiguration
			}
		}
		seen[node.ID] = true
		self, ok := node.Provider.(inference.SelfHostedProvider)
		if !ok || !self.SelfHosted() {
			return nil, ErrHostedDisabled
		}
		identity := (inference.Runner{Provider: node.Provider}).RuntimeIdentity()
		if identity == "" || d.runtime != "" && identity != d.runtime {
			return nil, ErrConfiguration
		}
		d.runtime = identity
	}
	return d, nil
}

// Run freezes one approved snapshot (at most MaxSpecBytes) before waiting. The
// caller must not mutate it concurrently while copied. Pending/uncertain jobs
// never dispatch elsewhere. Optional peer polling always denies fresh admission,
// even if the artifact disappears or its completed result expires.
func (d *Dispatcher) Run(ctx context.Context, spec inference.Spec) (out DispatchResult, err error) {
	if d == nil || ctx == nil {
		return out, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		return out, admission.ErrQueueFull
	}
	if !inference.SelfHosted(spec) {
		return out, ErrHostedDisabled
	}
	if _, err := inference.BuildPlan(spec); err != nil {
		return out, err
	}
	data, err := json.Marshal(spec)
	if err != nil || len(data) > MaxSpecBytes {
		return out, ErrConfiguration
	}
	spec = inference.Spec{}
	out.Nodes = make([]NodeStats, len(d.nodes))
	for i, node := range d.nodes {
		out.Nodes[i].ID = node.ID
	}
	wait := ctx
	if d.options.MaxWait > 0 {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, d.options.MaxWait)
		defer cancel()
	}
	peer := -1
	var peerFence inference.Result
	for {
		for i, node := range d.nodes {
			if peer >= 0 && peer != i {
				continue
			}
			if wait.Err() != nil {
				return out, errors.Join(err, wait.Err())
			}
			runner := inference.Runner{Broker: d.broker, Provider: node.Provider}
			if runner.RuntimeIdentity() != d.runtime {
				return out, ErrConfiguration
			}
			if self, ok := node.Provider.(inference.SelfHostedProvider); !ok || !self.SelfHosted() {
				return out, ErrHostedDisabled
			}
			var frozen inference.Spec
			if json.Unmarshal(data, &frozen) != nil {
				return out, ErrConfiguration
			}
			pollingPeer := peer >= 0
			admit := func(_ context.Context, s inference.Spec) (func(), error) {
				if pollingPeer {
					return nil, inference.ErrAdmissionDeferred
				}
				if wait.Err() != nil {
					return nil, errors.Join(inference.ErrAdmissionDeferred, wait.Err())
				}
				release, admitErr := node.Admission(wait, s)
				if wait.Err() != nil && (admitErr == nil || errors.Is(admitErr, inference.ErrAdmissionDeferred)) {
					admitErr = errors.Join(inference.ErrAdmissionDeferred, admitErr, wait.Err())
				}
				return release, admitErr
			}
			runCtx := ctx
			if pollingPeer {
				runCtx = wait // read-only; the deny gate cannot start generation
			}
			out.Result, err = runner.RunWithAdmission(runCtx, frozen, admit)
			stats := &out.Nodes[i]
			stats.Runs++
			if pollingPeer {
				stats.PeerPolls++
			}
			if m := out.Measurement; m != nil {
				stats.AdmissionWaitMS += m.PhaseMS["resource_admission"]
				if m.GenerationCalled {
					stats.GenerationCalls++
				}
			}
			if inference.PreClaimDeferred(out.Result, err) {
				stats.Deferrals++
				if pollingPeer {
					out.Result = peerFence
					return out, errors.Join(inference.ErrInFlight, err)
				}
				continue
			}
			waitForPeer := peerWaitSafe(out.Result, err)
			if pollingPeer && err != nil {
				out.Result = peerFence
				err = errors.Join(inference.ErrInFlight, err)
			}
			if !waitForPeer || !d.options.WaitForPeer || d.options.MaxWait == 0 {
				out.SelectedNode = node.ID
				return out, err
			}
			if peer < 0 {
				peerFence = out.Result
			}
			peer = i
			break
		}
		if d.options.MaxWait == 0 {
			return out, err
		}
		timer := time.NewTimer(d.options.PollInterval)
		select {
		case <-wait.Done():
			timer.Stop()
			return out, errors.Join(err, wait.Err())
		case <-timer.C:
		}
	}
}

// A provider can return ErrInFlight after starting work. Only a lookup fence or
// a lost claim CAS permits waiting; execution errors keep their original stop.
func peerWaitSafe(result inference.Result, err error) bool {
	m := result.Measurement
	if !errors.Is(err, inference.ErrInFlight) || m == nil || m.Outcome != "pending" || m.GenerationCalled || result.Fingerprint != "" || result.Durable {
		return false
	}
	if _, lookedUp := m.PhaseMS["lookup"]; !lookedUp {
		return false
	}
	if _, gated := m.PhaseMS["resource_admission"]; gated {
		if _, claimed := m.PhaseMS["admission"]; !claimed {
			return false
		}
	}
	for _, phase := range []string{"preflight", "reservation", "generation", "publication", "settlement", "recovery", "reuse_resolution"} {
		if _, found := m.PhaseMS[phase]; found {
			return false
		}
	}
	return true
}
