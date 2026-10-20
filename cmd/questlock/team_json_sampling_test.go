package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/team"
)

func TestTeamJSONPreparationPreservesInstalledSamplingForEveryRole(t *testing.T) {
	for _, command := range []string{"init", "suite"} {
		for _, profile := range []struct {
			name                      string
			think, canonical, release bool
		}{
			{"json", false, false, false},
			{"thinking", true, false, false},
			{"canonical", false, true, false},
			{"release", false, false, true},
			{"all", true, true, true},
		} {
			t.Run(command+"/"+profile.name, func(t *testing.T) {
				args := []string{command, "--model", "fixture:local", "--json-output"}
				if profile.think {
					args = append(args, "--think")
				}
				if profile.canonical {
					args = append(args, "--canonical-prompt")
				}
				if profile.release {
					args = append(args, "--release-model")
				}
				var out, diag bytes.Buffer
				lookups := 0
				err := teamCommandIOWithModelDigest(args, &out, &diag, func(ctx context.Context, model string) (string, error) {
					lookups++
					if model != "fixture:local" || ctx.Err() != nil {
						t.Fatal("unexpected metadata request", model, ctx.Err())
					}
					return strings.Repeat("a", 64), nil
				})
				if err != nil || lookups != 1 {
					t.Fatalf("offline JSON preparation failed: %v, %d lookups", err, lookups)
				}
				var requests []team.Request
				if command == "init" {
					var request team.Request
					if err := json.Unmarshal(out.Bytes(), &request); err != nil {
						t.Fatal(err)
					}
					requests = append(requests, request)
				} else {
					var suite team.Suite
					if err := json.Unmarshal(out.Bytes(), &suite); err != nil || len(suite.Cases) != 6 {
						t.Fatalf("invalid offline suite: %+v, %v", suite, err)
					}
					for _, c := range suite.Cases {
						requests = append(requests, c.Request)
					}
				}
				for _, request := range requests {
					if len(request.Reviewers) != 2 {
						t.Fatal("preparation lost reviewers")
					}
					for _, role := range append([]team.Role{request.Master}, request.Reviewers...) {
						options := role.Ollama
						if options == nil || options.Format != "json" || options.Temperature != nil {
							t.Fatalf("JSON preparation overrode installed sampling for %s: %+v", role.ID, options)
						}
						if profile.think != (options.Think != nil && *options.Think) || profile.canonical != (options.PromptMode == "bounded_text") || profile.release != (options.KeepAliveSeconds != nil && *options.KeepAliveSeconds == 0) {
							t.Fatalf("JSON preparation changed explicit policy for %s: %+v", role.ID, options)
						}
						if role.MaxOutputTokens != 512 {
							t.Fatal("JSON preparation changed default output allowance")
						}
					}
				}
			})
		}
	}
}

func TestTeamJSONOutputHelpDescribesInstalledSamplingDefaults(t *testing.T) {
	for _, command := range []string{"init", "suite"} {
		var out, diag bytes.Buffer
		if err := teamCommandIO([]string{command, "--help"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diag.String(), "JSON output using installed sampling defaults") || strings.Contains(diag.String(), "temperature zero") {
			t.Fatalf("JSON flag help does not describe the default sampling policy: %s", diag.String())
		}
	}
}
