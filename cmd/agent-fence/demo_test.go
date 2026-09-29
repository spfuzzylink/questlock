package main

import (
	"os"
	"testing"
)

// Let the demo launch this test binary as a real broker/worker subprocess.
func TestMain(m *testing.M) {
	if os.Getenv("AGENT_FENCE_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWorkerAndBrokerCrashRecovery(t *testing.T) {
	t.Setenv("AGENT_FENCE_TEST_PROCESS", "1")
	if err := demo(); err != nil {
		t.Fatal(err)
	}
}
