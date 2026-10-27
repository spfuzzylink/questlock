package delegation_test

import (
	"testing"

	"github.com/spfuzzylink/questlock/delegation"
	"github.com/spfuzzylink/questlock/internal/semanticquest"
)

func TestShadowPolicyNeverChangesGenerationIdentity(t *testing.T) {
	r := semanticquest.Request(semanticquest.Cases()[0])
	r.SemanticShadow = nil
	original := delegation.RequestDigest(r)
	r.SemanticShadow = semanticquest.Request(semanticquest.Cases()[0]).SemanticShadow
	if delegation.RequestDigest(r) != original {
		t.Fatal("shadow opt-in changed exact snapshot identity")
	}
	for _, change := range []func(){func() { r.SemanticShadow.Family = "other" }, func() { r.SemanticShadow.Domain = "other" }, func() { r.SemanticShadow.MinSimilarity = .99 }, func() { r.SemanticShadow.MaxEntries = 42 }} {
		change()
		if delegation.RequestDigest(r) != original {
			t.Fatal("shadow settings changed generated work identity")
		}
	}
}

func TestShadowContractBindsEveryApprovedBoundary(t *testing.T) {
	base := func() delegation.Request { return semanticquest.Request(semanticquest.Cases()[0]) }
	r := base()
	runtime := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	partition, err := delegation.ShadowPartition(r, r.Policy.Local, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*delegation.Request){
		"domain":        func(r *delegation.Request) { r.SemanticShadow.Domain = "other" },
		"family":        func(r *delegation.Request) { r.SemanticShadow.Family = "other" },
		"evidence":      func(r *delegation.Request) { r.SemanticShadow.EvidenceRevision = "other" },
		"variable":      func(r *delegation.Request) { r.SemanticShadow.Bindings["entity"] = "other" },
		"instructions":  func(r *delegation.Request) { r.Spec.Instructions += " New constraint." },
		"source":        func(r *delegation.Request) { r.Spec.Context[0].Text += " New fact." },
		"check":         func(r *delegation.Request) { r.Checks[0].Text = "changed" },
		"model":         func(r *delegation.Request) { r.Policy.Local.Spec.Model = "other" },
		"task_revision": func(r *delegation.Request) { r.Spec.Revision = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			r := base()
			change(&r)
			p, err := delegation.ShadowPartition(r, r.Policy.Local, runtime)
			if err != nil || p == partition {
				t.Fatalf("boundary unbound: %s %v", p, err)
			}
		})
	}
	r = base()
	r.Spec.Input = "How can I reset my password?"
	same, err := delegation.ShadowPartition(r, r.Policy.Local, runtime)
	if err != nil || same != partition {
		t.Fatal("paraphrase changed approved contract", err)
	}
	other, err := delegation.ShadowPartition(r, r.Policy.Local, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil || other == partition {
		t.Fatal("runtime not bound", err)
	}
}
