package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemOneCommandRejectsUnsafeOrIncompleteInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(`{"schema":"questlock.system-one.choice.v1","model":"remote-cloud:latest"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		nil, {"choose"}, {"choose", "--request", path},
		{"choose", "--request", path, "--python", "/unavailable", "--provider", "remote"},
		{"choose", "--request", path, "--python", "/unavailable", "extra"},
		{"choose", "--request", path, "--python", "/unavailable"},
	} {
		var output, diagnostics bytes.Buffer
		if err := systemOneCommandIO(args, &output, &diagnostics); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if output.Len() != 0 {
			t.Fatalf("invalid request produced a decision: %s", output.String())
		}
	}
}

func TestSystemOneHelp(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if err := systemOneCommandIO([]string{"choose", "--help"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "120s") {
		t.Fatal("help omitted deadline bound")
	}
}
