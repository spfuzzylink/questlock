package team

import (
	"strconv"
	"testing"

	"github.com/spfuzzylink/questlock/inference"
)

func TestRoleOutputTokenLimitsDependOnDriver(t *testing.T) {
	for _, profile := range []struct{ driver, think string }{{"ollama", ""}, {"ollama", "unset"}, {"ollama", "false"}, {"ollama", "true"}, {"codex-oss", ""}} {
		for _, role := range []string{"master", "reviewer"} {
			for _, tokens := range []int64{-1, 15, 16, 4096, 4097, 8192, 8193} {
				t.Run(profile.driver+"/think="+profile.think+"/"+role+"/"+strconv.FormatInt(tokens, 10), func(t *testing.T) {
					r := fixture()
					target := &r.Master
					if role == "reviewer" {
						target = &r.Reviewers[0]
					}
					target.Driver, target.MaxOutputTokens = profile.driver, tokens
					if profile.think == "unset" {
						target.Ollama = &inference.OllamaOptions{}
					} else if profile.think != "" {
						think := profile.think == "true"
						target.Ollama = &inference.OllamaOptions{Think: &think}
					}
					valid := tokens >= 16 && tokens <= 8192 && (tokens <= 4096 || (profile.driver == "ollama" && profile.think == "true"))
					if err := Validate(r); (err == nil) != valid {
						t.Fatalf("driver %s output allowance %d: %v", profile.driver, tokens, err)
					}
					if valid {
						request, err := workerRequest(r, *target, `{}`)
						if err != nil || request.Policy.Local.Spec.MaxOutputTokens != tokens {
							t.Fatalf("approved allowance was changed in worker request: %+v, %v", request, err)
						}
					}
				})
			}
		}
	}
}
