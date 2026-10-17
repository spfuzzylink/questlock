// Package onboardingclient prepares complete assistant settings in a private,
// isolated home. It never installs a candidate or writes an active config.
package onboardingclient

import (
	"context"
	"os"
)

type Client string

const (
	Codex         Client = "codex"
	ClaudeCode    Client = "claude-code"
	ClaudeDesktop Client = "claude-desktop"
)

type Options struct {
	Client        Client
	HomeDir       string
	CodexHome     string
	ConfigPath    string
	Executable    string
	QuestlockPath string
	Workspace     string
	StateDir      string
	Runner        Runner // Optional test/host injection; nil runs native binaries.
	// SkipNativeMCPRead keeps Inspect from running Claude's MCP get command.
	// Exact static settings are still checked; native validation stays false.
	SkipNativeMCPRead bool
	live              bool
}

// Validation separates a syntactically preserved candidate from a native client
// read. Desktop has no documented isolated config reader and always requires
// native application verification before an active-file transaction can commit.
type Validation struct {
	Status             string `json:"status"`
	NativeValidated    bool   `json:"native_validated"`
	PreservedSettings  bool   `json:"preserved_settings"`
	ExactServer        bool   `json:"exact_server"`
	NotifyPreserved    bool   `json:"notify_preserved"`
	RequiresNativeRead bool   `json:"requires_native_read"`
}

type Candidate struct {
	Client          Client     `json:"client"`
	ActivePath      string     `json:"active_path"`
	Path            string     `json:"path"`
	IsolationDir    string     `json:"isolation_dir"`
	OriginalSHA256  string     `json:"original_sha256"`
	CandidateSHA256 string     `json:"candidate_sha256"`
	OriginalExists  bool       `json:"original_exists"`
	Validation      Validation `json:"validation"`
	options         Options
	original        map[string]any
	cleanupDir      string
	effective       map[string]any
}

// Close removes private copies of complete settings, which can contain secrets.
func (c *Candidate) Close() error {
	if c == nil || c.cleanupDir == "" {
		return nil
	}
	return os.RemoveAll(c.cleanupDir)
}

// Report contains redacted facts only. Native command output and settings values
// must never appear in diagnostic output, including native command failures.
type Report struct {
	Client          Client     `json:"client"`
	ConfigPath      string     `json:"config_path"`
	Executable      string     `json:"executable,omitempty"`
	ClientAvailable bool       `json:"client_available"`
	ConfigExists    bool       `json:"config_exists"`
	ConfigSHA256    string     `json:"config_sha256,omitempty"`
	ServerPresent   bool       `json:"server_present"`
	OtherServers    int        `json:"other_servers"`
	NotifyArguments int        `json:"notify_arguments,omitempty"`
	Status          string     `json:"status"`
	Validation      Validation `json:"validation"`
}

type Command struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
}

// Runner runs only configuration commands. CodexRead performs initialize,
// initialized, then config/read with includeLayers=true; it starts no thread.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
	CodexRead(context.Context, Command) (CodexSnapshot, error)
}

type CodexSnapshot struct {
	Config map[string]any `json:"config"`
	Layers []struct {
		Name struct {
			Type string `json:"type"`
			File string `json:"file"`
		} `json:"name"`
		Config map[string]any `json:"config"`
	} `json:"layers"`
}
