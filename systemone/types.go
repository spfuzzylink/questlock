// Package systemone offers an advisory typed choice using the official TypeSafe
// adapter and explicitly selected local Ollama weights. It never runs an action.
package systemone

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	Schema          = "questlock.system-one.choice.v1"
	AdapterVersion  = "0.2.1"
	AdapterCommit   = "e1d4cc938204b22fc5a3c3aca7044072fe3f712d"
	OllamaURL       = "http://127.0.0.1:11434"
	MaxRequestBytes = 16384
	MaxContextBytes = 4096
	MaxOutputTokens = 256
)

type Choice struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Policy thresholds operate on uncalibrated model scores. They are caller
// heuristics, not a probability of correctness or authorization to perform work.
type Policy struct {
	MinScore  float64 `json:"min_score"`
	MinMargin float64 `json:"min_margin"`
}

type Request struct {
	Schema   string   `json:"schema"`
	Model    string   `json:"model"`
	State    string   `json:"state"`
	Question string   `json:"question"`
	Choices  []Choice `json:"choices"`
	Policy   *Policy  `json:"policy"`
}

type Usage struct {
	InputTokens                  *int64  `json:"input_tokens"`
	OutputTokens                 *int64  `json:"output_tokens"`
	LocalModelCalls              *int    `json:"local_model_calls"`
	Retries                      int     `json:"retries"`
	AdapterLatencyMS             float64 `json:"adapter_latency_ms"`
	ElapsedMS                    float64 `json:"elapsed_ms"`
	ProviderCostUSD              float64 `json:"provider_cost_usd"`
	CostBasis                    string  `json:"cost_basis"`
	HostedAssistantUsageIncluded bool    `json:"hosted_assistant_usage_included"`
}

type Result struct {
	Schema         string             `json:"schema"`
	Status         string             `json:"status"`
	Reason         string             `json:"reason"`
	ChoiceID       string             `json:"choice_id,omitempty"`
	Scores         map[string]float64 `json:"uncalibrated_scores,omitempty"`
	AdvisoryOnly   bool               `json:"advisory_only"`
	RequestSHA256  string             `json:"request_sha256"`
	Model          string             `json:"model"`
	ModelDigest    string             `json:"model_digest,omitempty"`
	AdapterVersion string             `json:"adapter_version,omitempty"`
	Usage          Usage              `json:"usage"`
}

var labelPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var modelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{0,127}$`)

func (r Request) Validate() error {
	if r.Schema != Schema || !modelPattern.MatchString(r.Model) || strings.Contains(strings.ToLower(r.Model), "cloud") || !strings.Contains(r.Model[strings.LastIndex(r.Model, "/")+1:], ":") {
		return errors.New("request requires the choice schema and an explicit local model tag without cloud markers")
	}
	name, tag, _ := strings.Cut(r.Model[strings.LastIndex(r.Model, "/")+1:], ":")
	if name == "" || tag == "" || strings.Contains(tag, ":") {
		return errors.New("explicit local model name and nonempty tag required")
	}
	for _, part := range strings.Split(r.Model, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid local model name")
		}
	}
	if !text(r.State) || !text(r.Question) || len(r.Question) > 512 || len(r.Choices) < 2 || len(r.Choices) > 8 {
		return errors.New("request requires bounded state, question, and 2 to 8 choices")
	}
	total := len(r.State) + len(r.Question)
	seen := map[string]bool{}
	for _, choice := range r.Choices {
		if !labelPattern.MatchString(choice.ID) || seen[choice.ID] || !text(choice.Description) || len(choice.Description) > 512 {
			return errors.New("choices require unique simple IDs and bounded descriptions")
		}
		seen[choice.ID] = true
		total += len(choice.ID) + len(choice.Description)
	}
	if total > MaxContextBytes {
		return errors.New("state and choice descriptions exceed the 4096-byte context bound")
	}
	if r.Policy == nil || !unit(r.Policy.MinScore) || r.Policy.MinScore == 0 || !unit(r.Policy.MinMargin) || r.Policy.MinMargin == 0 {
		return errors.New("explicit score and margin heuristics must be greater than zero and at most one")
	}
	return nil
}

func text(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
func unit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func decide(request Request, result *Result, scores map[string]float64) {
	if len(scores) != len(request.Choices) {
		result.Reason = "malformed_scores"
		return
	}
	top, second, sum := -1.0, -1.0, 0.0
	winner := ""
	for _, choice := range request.Choices {
		score, ok := scores[choice.ID]
		if !ok || !unit(score) {
			result.Reason = "malformed_scores"
			return
		}
		sum += score
		if score > top {
			second, top, winner = top, score, choice.ID
		} else if score > second {
			second = score
		}
	}
	if math.Abs(sum-1) > 1e-6 {
		result.Reason = "malformed_scores"
		return
	}
	result.Scores = scores
	if winner == "abstain" {
		result.Reason = "model_abstained"
		return
	}
	if top < request.Policy.MinScore {
		result.Reason = "score_below_heuristic"
		return
	}
	if top-second < request.Policy.MinMargin {
		result.Reason = "ambiguous_scores"
		return
	}
	result.Status, result.Reason, result.ChoiceID = "chosen", "advisory_choice", winner
}
