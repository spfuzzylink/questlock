package delegation_test

import (
	"context"
	"os"
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
)

func TestPersonalLocalOnlyPresetDoesNotAuthorizePaidWork(t *testing.T) {
	file, err := os.Open("../examples/preferences/local-only.request.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	request, err := delegation.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if request.Policy.Escalation != nil || request.Policy.Local.Spec.Provider != "ollama" || request.Policy.Local.Spec.MaxCostUSD != 0 || request.Policy.Local.Spec.DailyBudgetUSD != 0 {
		t.Fatal("local-only example must not grant paid escalation")
	}
	if err := delegation.Validate(request, false); err != nil {
		t.Fatal(err)
	}
	// Adding a hosted tier to the file cannot grant the calling process permission.
	hosted := request.Policy.Local
	hosted.ID, hosted.Spec.Provider, hosted.Spec.Model = "unapproved-hosted", "openai", "example-model"
	request.Policy.Escalation = &hosted
	factoryCalled := false
	router := delegation.Router{Factory: func(delegation.Tier) (delegation.Runner, error) {
		factoryCalled = true
		return nil, nil
	}}
	if _, err := router.Run(context.Background(), request); err == nil || factoryCalled {
		t.Fatal("unapproved paid policy must fail before any provider is constructed")
	}
}
