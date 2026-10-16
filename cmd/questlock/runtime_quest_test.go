package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeQuestOffline(t *testing.T) {
	for _, key := range []string{"QUESTLOCK_TOKEN", "QUESTLOCK_TOKEN_FILE", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "SGLANG_API_KEY"} {
		t.Setenv(key, "")
	}
	var output, help bytes.Buffer
	if err := modelCommandIO([]string{"runtime-quest"}, &output, &help); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "PASS  ") != 6 || !strings.Contains(output.String(), "protocol_fixture") || !strings.Contains(output.String(), "GPU performance and simulated latency were not measured") {
		t.Fatal(output.String())
	}
}

func TestRuntimeInspectIsReadOnly(t *testing.T) {
	for _, profile := range []string{"dense", "hisparse"} {
		t.Run(profile, func(t *testing.T) {
			s := newRuntimeQuestServer(profile, false)
			defer s.server.Close()
			path := filepath.Join(t.TempDir(), "spec.json")
			data, err := json.Marshal(runtimeQuestSpec(profile, "approved-namespace"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var output, help bytes.Buffer
			if err = modelCommandIO([]string{"runtime-inspect", "--spec", path, "--api-base-url", s.server.URL}, &output, &help); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Capabilities struct {
					GenerationSupported bool `json:"generation_supported"`
				} `json:"capabilities"`
				GPUValidated bool `json:"gpu_validated"`
			}
			if err = json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Capabilities.GenerationSupported != (profile != "hisparse") || result.GPUValidated || s.calls.Load() != 0 {
				t.Fatal("inspection generated or misrepresented capability")
			}
			for _, marker := range []string{s.server.URL, "/models/pinned", "approved-namespace", "Approved fixture task"} {
				if strings.Contains(output.String(), marker) {
					t.Fatal("inspection exposed content or endpoint")
				}
			}
		})
	}
}
