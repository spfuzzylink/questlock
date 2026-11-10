package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

func TestOfflineEntryDoesNotRequireEndpointsOrFiles(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}} {
		var out bytes.Buffer
		if err := run(args, &out); err != nil || out.Len() == 0 {
			t.Fatalf("offline entry: %v, %q", err, out.String())
		}
	}
}

func TestLiveRequiresLiteralLoopbackOriginsBeforeFileAccess(t *testing.T) {
	for _, endpoint := range []string{"", "http://localhost:11434", "http://192.0.2.1:11434", "http://127.0.0.1:11434/path", "http://user@127.0.0.1:11434", "http://127.0.0.1:11434?x=1"} {
		err := run([]string{"--live", "--broker", endpoint, "--ollama", "http://127.0.0.1:11434"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("endpoint %q: %v", endpoint, err)
		}
	}
}

func TestSixCaseMeasurementUsesVerifierNotQuestionAllowlist(t *testing.T) {
	var set caseSet
	if err := json.Unmarshal(casesJSON, &set); err != nil || len(set.Cases) != 6 {
		t.Fatal("six-case snapshot missing", err)
	}
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if seen[c.ID] || len(c.Paraphrases) != 2 || len(c.Checks) != 2 {
			t.Fatal("invalid independent case", c.ID)
		}
		seen[c.ID] = true
		for _, input := range append([]string{c.OriginalQuestion}, c.Paraphrases...) {
			r := requestFor(set.Revision, c, input, nil)
			if r.Spec.Input != input || r.Policy.Escalation != nil || r.SemanticReuse == nil {
				t.Fatal("input changed or fallback enabled", c.ID)
			}
			cfg, err := semantic.NormalizeReuseConfig(*r.SemanticReuse)
			if err != nil || cfg.VerifierPolicy != "factorized-v1" || cfg.VerifierMaxInputTokens != 16384 {
				t.Fatal("measurement policy drift", c.ID, err)
			}
		}
	}
}
