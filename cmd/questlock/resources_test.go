package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spfuzzylink/questlock/admission"
	"github.com/spfuzzylink/questlock/internal/localruntime"
	"golang.org/x/sys/unix"
)

func TestResourcesInspectIsReadOnlyAndConservative(t *testing.T) {
	for _, known := range []bool{true, false} {
		var out, diag bytes.Buffer
		source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
			s := admission.Snapshot{At: time.Now(), Known: known, Source: "fixture", CPUCapacityMilli: 4000, LoadCapacityMilli: 8000, AvailableMemoryBytes: 2 << 30, MemoryPressure: admission.High}
			if !known {
				return s, admission.ErrSignals
			}
			return s, nil
		})
		e := resourcesCommandIO([]string{"inspect"}, &out, &diag, source)
		if known && e != nil || !known && !errors.Is(e, admission.ErrSignals) {
			t.Fatal(e)
		}
		var report struct {
			Admission admission.Status `json:"admission"`
			ReadOnly  bool             `json:"read_only"`
		}
		if e = json.Unmarshal(out.Bytes(), &report); e != nil || !report.ReadOnly || report.Admission.ExpensiveWorkerLimit != 0 {
			t.Fatal(report, e)
		}
	}
}

func TestAdmissionPolicyFileRefusesBlockingAndNonRegularInputs(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "policy.json")
	data, _ := json.Marshal(admission.DefaultConfig())
	if e := os.WriteFile(valid, data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := loadAdmissionConfig(valid); e != nil {
		t.Fatal("generated complete policy rejected", e)
	}
	fifo := filepath.Join(dir, "no-writer.fifo")
	if e := unix.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	link, oversized := filepath.Join(dir, "link.json"), filepath.Join(dir, "oversized.json")
	if e := os.Symlink(valid, link); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(oversized, bytes.Repeat([]byte{' '}, (64<<10)+1), 0600); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{fifo, dir, link, oversized} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			done := make(chan error, 1)
			go func() { _, e := loadAdmissionConfig(path); done <- e }()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("unsafe policy input accepted")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("policy open bypassed command deadlines")
			}
		})
	}
}

func TestResourceQueueQuestExecutesDurableWork(t *testing.T) {
	var out bytes.Buffer
	if e := admissionQuest(&out); e != nil {
		t.Fatal(e, out.String())
	}
	if strings.Count(out.String(), "PASS ") != 4 || !strings.Contains(out.String(), "QUEST CLEARED") {
		t.Fatal(out.String())
	}
}

func TestQueueCLIValidatesBatchAndExecutesOnceWithExactReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture, e := newModelQuestFixture(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer fixture.close()
	workspace := t.TempDir()
	request, _ := json.Marshal(firstRequest())
	config := admission.DefaultConfig()
	config.SampleInterval, config.RampInterval = 10*time.Millisecond, 10*time.Millisecond
	config.RecoverySamples = 2
	policy, _ := json.Marshal(config)
	for name, data := range map[string][]byte{"approved.json": request, "policy.json": policy, "invalid.json": []byte(`{"schema":1}`)} {
		if e = os.WriteFile(filepath.Join(workspace, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	source := admission.SourceFunc(func(context.Context) (admission.Snapshot, error) {
		return admission.Snapshot{At: time.Now(), Known: true, Source: "explicit_fixture", CPUCapacityMilli: 8000, LoadCapacityMilli: 8000, AvailableMemoryBytes: 2 << 30, MemoryPressure: admission.Normal}, nil
	})
	starts := 0
	ensure := func(context.Context, localruntime.Options) (localruntime.Handle, error) {
		starts++
		return localruntime.Handle{Client: fixture.clients[0]}, nil
	}
	var out, diag bytes.Buffer
	base := []string{"revise", "--workspace", workspace, "--state-dir", filepath.Join(workspace, "runtime"), "--config", filepath.Join(workspace, "policy.json")}
	if e = queueCommandIO(append(append([]string{}, base...), "--request", "approved.json", "--request", "invalid.json"), &out, &diag, source, ensure); e == nil || starts != 0 {
		t.Fatal("invalid batch reached execution", e, starts)
	}
	out.Reset()
	if e = queueCommandIO(append(base, "--request", "approved.json", "--request", "approved.json"), &out, &diag, source, ensure); e != nil {
		t.Fatal(e, out.String(), diag.String())
	}
	decoder := json.NewDecoder(&out)
	fresh, reused := 0, 0
	jobs := map[string]bool{}
	for i := 0; i < 2; i++ {
		var report struct {
			JobID  string `json:"job_id"`
			Result struct {
				Status, Text     string
				Durable, Reused  bool
				GenerationCalled bool `json:"generation_called"`
			} `json:"result"`
		}
		if e = decoder.Decode(&report); e != nil || report.Result.Status != "completed" || !report.Result.Durable || report.Result.GenerationCalled || report.Result.Text != `{"status":"ready","model_calls":0}` || jobs[report.JobID] || (report.JobID != "request-1" && report.JobID != "request-2") {
			t.Fatal("CLI lost durable, generation-free publication or exact reuse", report, e)
		}
		jobs[report.JobID] = true
		if report.Result.Reused {
			reused++
		} else {
			fresh++
		}
	}
	// Concurrent completions may arrive in either order. Exactly one publish
	// must be fresh and the other must reuse its durable receipt.
	if fresh != 1 || reused != 1 {
		t.Fatal("queue lost single fresh publication or exact reuse", fresh, reused)
	}
	var summary struct {
		Status           string
		Admitted, Failed int
		NotAdmitted      int `json:"not_admitted"`
		ModelCalls       int `json:"model_calls"`
		Outcomes         []admission.Outcome
	}
	if e = decoder.Decode(&summary); e != nil || summary.Status != "finished" || summary.Admitted != 2 || summary.Failed != 0 || summary.NotAdmitted != 0 || summary.ModelCalls != 0 || starts != 1 || len(summary.Outcomes) != 2 || summary.Outcomes[0].Status != "completed" || summary.Outcomes[1].ID != "request-2" {
		t.Fatal(summary, e, starts)
	}
}
