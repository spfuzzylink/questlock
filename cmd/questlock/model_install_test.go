package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestModelInstallParsesAgents(t *testing.T) {
	for _, test := range []struct {
		value string
		want  []string
	}{
		{"codex,claude", []string{"codex", "claude"}},
		{"claude,codex", []string{"claude", "codex"}},
		{" codex ", []string{"codex"}},
		{"claude", []string{"claude"}},
	} {
		got, err := parseInstallAgents(test.value)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseInstallAgents(%q) = %v, %v", test.value, got, err)
		}
	}
	for _, value := range []string{"", "codex,", ",claude", "codex,codex", "claude,claude", "private-value"} {
		if _, err := parseInstallAgents(value); err == nil || strings.Contains(err.Error(), "private-") {
			t.Errorf("invalid agent selection accepted or exposed: %q, %v", value, err)
		}
	}
}

func TestModelInstallHelpAndInvalidFlagsNeedNoHostMutation(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CODEX_HOME", "")
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		var output, help bytes.Buffer
		if err := modelInstallCommand(args, &output, &help); err != nil || help.Len() == 0 || output.Len() != 0 {
			t.Errorf("help %v failed: %v", args, err)
		}
	}
	for _, args := range [][]string{{"positional"}, {"--agents", "unknown"}, {"--unknown=private-value"}, {"--replace=private-value"}} {
		var output, help bytes.Buffer
		if err := modelInstallCommand(args, &output, &help); err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(help.String(), "private-") {
			t.Errorf("invalid arguments accepted or exposed: %v, %v", args, err)
		}
	}
}
