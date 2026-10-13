package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestModelQuestOfflineScenarios(t *testing.T) {
	// The public command must run the six real-storage assertions without
	// consulting broker credentials or provider API keys.
	t.Setenv("QUESTLOCK_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	var output, help bytes.Buffer
	if err := modelCommandIO([]string{"quest"}, &output, &help); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(output.String(), "PASS  "); got != 6 {
		t.Fatalf("expected all six scenario assertions to pass, got %d:\n%s", got, output.String())
	}
	if !strings.Contains(output.String(), "QUEST CLEARED") || help.Len() != 0 {
		t.Fatalf("unexpected command output: stdout=%q help=%q", output.String(), help.String())
	}
}
