package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/evaluation"
)

func TestLoadStrictBoundedAndPermissionCannotComeFromJSON(t *testing.T) {
	valid, _ := json.Marshal(testRequest())
	for _, data := range []string{
		strings.Replace(string(valid), `"spec":`, `"allow_paid":true,"spec":`, 1),
		strings.Replace(string(valid), `"task":"release-review"`, `"task":"release-review","task":"other"`, 1),
		strings.Replace(string(valid), `"local":`, `"third_tier":{},"local":`, 1),
		string(valid) + `{}`,
		strings.Repeat(" ", 1<<20) + string(valid),
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
		string([]byte{'"', 0xff, '"'}),
	} {
		if _, err := Load(strings.NewReader(data)); err == nil {
			t.Fatal("ambiguous/unauthorized/oversized request JSON accepted")
		}
	}
	request, err := Load(strings.NewReader(string(valid)))
	if err != nil || Validate(request, false) != nil {
		t.Fatalf("valid local request rejected: %v", err)
	}
	hosted, _ := json.Marshal(paidRequest())
	request, err = Load(strings.NewReader(string(hosted)))
	if err != nil || Validate(request, false) == nil {
		t.Fatal("file contents bypassed caller paid authorization")
	}
}

func TestWorkerResourceAndQualityBoundsApplyBeforeAnyFactory(t *testing.T) {
	for _, kind := range []string{"unchecked", "too_many_checks", "input_tokens", "output_tokens", "context_bytes", "base_stream"} {
		t.Run(kind, func(t *testing.T) {
			request := testRequest()
			switch kind {
			case "unchecked":
				request.Checks = nil
			case "too_many_checks":
				request.Checks = make([]evaluation.Check, 65)
			case "input_tokens":
				request.Policy.Local.Spec.MaxInputTokens = 32769
			case "output_tokens":
				request.Policy.Local.Spec.MaxOutputTokens = 4097
			case "context_bytes":
				request.Policy.Local.Spec.MaxContextBytes = 131073
			case "base_stream":
				request.Spec.Stream = true
			}
			factories := 0
			_, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return nil, nil }}).Run(context.Background(), request)
			if err == nil || factories != 0 {
				t.Fatalf("unchecked/unbounded task ran: %v calls=%d", err, factories)
			}
		})
	}
	request := testRequest()
	request.Policy.Local.Spec.Stream = true
	if err := Validate(request, false); err != nil {
		t.Fatalf("explicit local stream tier rejected: %v", err)
	}
}

func TestCancellationAndFactoryErrorsDoNotEscalateOrLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factories := 0
	result, err := (Router{Factory: func(Tier) (Runner, error) { factories++; return nil, nil }}).Run(ctx, testRequest())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrStopped) || result.Status != "stopped" || factories != 0 {
		t.Fatalf("canceled request started: %v %+v", err, result)
	}
	secret := "private-upstream-factory-message"
	result, err = (Router{Factory: func(Tier) (Runner, error) { factories++; return nil, errors.New(secret) }}).Run(context.Background(), testRequest())
	encoded, _ := json.Marshal(result)
	if !errors.Is(err, ErrStopped) || factories != 1 || strings.Contains(err.Error(), secret) || strings.Contains(string(encoded), secret) || result.Response != nil {
		t.Fatalf("factory error leaked/escalated: %v %s", err, encoded)
	}
}
