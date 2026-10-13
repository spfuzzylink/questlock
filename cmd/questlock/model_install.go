package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spfuzzylink/questlock/internal/agentsetup"
)

func modelInstallCommand(args []string, output, helpOutput io.Writer) error {
	f := flag.NewFlagSet("model install", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	agents := f.String("agents", "codex,claude", "install workflow skills for codex, claude, or both")
	replace := f.Bool("replace", false, "replace differing existing Questlock skill contents")
	f.Usage = func() {
		f.SetOutput(helpOutput)
		fmt.Fprintln(helpOutput, "Usage: questlock model install [--agents codex,claude] [--replace]")
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid model install flags; use model install --help")
	}
	if f.NArg() != 0 {
		return errors.New("model install does not accept positional arguments")
	}
	selected, err := parseInstallAgents(*agents)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return errors.New("cannot determine the user home directory")
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("cannot locate the running executable")
	}
	result, err := agentsetup.Install(agentsetup.Options{
		Home: home, Executable: executable, CodexHome: os.Getenv("CODEX_HOME"), Agents: selected, Replace: *replace,
	})
	if err != nil {
		return err
	}
	return writeModelJSON(output, result)
}

func parseInstallAgents(value string) ([]string, error) {
	var agents []string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		agent := strings.TrimSpace(part)
		if (agent != "codex" && agent != "claude") || seen[agent] {
			return nil, errors.New("--agents must name codex, claude, or both once")
		}
		seen[agent] = true
		agents = append(agents, agent)
	}
	return agents, nil
}
