package main

import (
	"os"
	"strings"
	"testing"
)

// Let the demo launch this test binary as a real broker/worker subprocess.
func TestMain(m *testing.M) {
	if os.Getenv("QUESTLOCK_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestQuestChildrenDoNotInheritHostCredentials(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-not-a-credential")
	t.Setenv("GITHUB_TOKEN", "fixture-not-a-credential")
	t.Setenv("QUESTLOCK_TEST_PROCESS", "1")
	env := demoEnvironment("QUESTLOCK_DEMO_TOKEN=fixture-only")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY=") || strings.Contains(joined, "GITHUB_TOKEN=") {
		t.Fatal("quest child environment leaked inherited credential variables")
	}
	if !strings.Contains(joined, "QUESTLOCK_DEMO_TOKEN=fixture-only") || !strings.Contains(joined, "QUESTLOCK_TEST_PROCESS=1") {
		t.Fatal("quest child environment lost explicit fixture configuration")
	}
}

func TestWorkerAndBrokerCrashRecovery(t *testing.T) {
	t.Setenv("QUESTLOCK_TEST_PROCESS", "1")
	if err := demo(); err != nil {
		t.Fatal(err)
	}
}
