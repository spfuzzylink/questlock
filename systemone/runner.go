package systemone

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spfuzzylink/questlock/inference"
)

//go:embed bridge.py
var bridge string

type Runner struct {
	Python  string        // Absolute interpreter from an explicitly prepared pinned venv.
	Timeout time.Duration // Default 60s; hard maximum 120s including discovery.
	inspect func(context.Context, string) (string, error)
	execute func(context.Context, string, []byte) ([]byte, error)
}

type bridgeResponse struct {
	Schema         string             `json:"schema"`
	Status         string             `json:"status"`
	Reason         string             `json:"reason"`
	Model          string             `json:"model"`
	AdapterVersion string             `json:"adapter_version"`
	Scores         map[string]float64 `json:"scores"`
	InputTokens    *int64             `json:"input_tokens"`
	OutputTokens   *int64             `json:"output_tokens"`
	Calls          int                `json:"calls"`
	Retries        int                `json:"retries"`
	LatencyMS      float64            `json:"latency_ms"`
}

// Choose makes at most one local model request. No action, write authorization,
// provider credential, retry, paid fallback, or automatic model pull is offered.
// Valid requests can return abstained with nil error; callers must check Status.
func (runner Runner) Choose(ctx context.Context, request Request) (result Result, err error) {
	start := time.Now()
	zeroCalls := 0
	result = Result{Schema: Schema, Status: "abstained", Reason: "invalid_request", AdvisoryOnly: true, Model: request.Model, Usage: Usage{CostBasis: "local_no_provider_charge", LocalModelCalls: &zeroCalls}}
	defer func() { result.Usage.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000 }()
	if err := request.Validate(); err != nil {
		return result, err
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > MaxRequestBytes {
		return result, errors.New("cannot encode bounded choice request")
	}
	digest := sha256.Sum256(data)
	result.RequestSHA256 = hex.EncodeToString(digest[:])
	if runner.Timeout == 0 {
		runner.Timeout = 60 * time.Second
	}
	if runner.Timeout <= 0 || runner.Timeout > 120*time.Second {
		return result, errors.New("typed-choice timeout must be greater than zero and at most 120s")
	}
	if !filepath.IsAbs(runner.Python) {
		return result, errors.New("an absolute interpreter from the pinned adapter environment is required")
	}
	if info, e := os.Stat(runner.Python); e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return result, errors.New("adapter interpreter is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, runner.Timeout)
	defer cancel()
	if runner.inspect == nil {
		runner.inspect = localModelDigest
	}
	if runner.execute == nil {
		runner.execute = runBridge
	}
	result.Reason = "model_unavailable"
	result.ModelDigest, err = runner.inspect(ctx, request.Model)
	if err != nil {
		return result, nil
	}
	result.Usage.LocalModelCalls = nil // A killed or malformed bridge can leave its call count unknown.
	output, err := runner.execute(ctx, runner.Python, data)
	if err != nil {
		result.Reason = "adapter_unavailable"
		if ctx.Err() != nil {
			result.Reason = "operation_timeout"
		}
		return result, nil
	}
	var response bridgeResponse
	result.Reason = "malformed_adapter_response"
	if len(output) > 16384 || strictJSON(output, &response) != nil {
		return result, nil
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(output, &fields) // strictJSON already checked syntax and keys.
	if len(fields) != 11 {
		return result, nil
	}
	for _, name := range []string{"schema", "status", "reason", "model", "adapter_version", "calls", "retries", "latency_ms"} {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return result, nil
		}
	}
	result.AdapterVersion = response.AdapterVersion
	if response.Schema != "questlock.system-one.bridge.v1" || response.Model != request.Model || response.Calls < 0 || response.Calls > 1 || response.Retries != 0 || math.IsNaN(response.LatencyMS) || math.IsInf(response.LatencyMS, 0) || response.LatencyMS < 0 {
		return result, nil
	}
	result.Usage.LocalModelCalls, result.Usage.Retries, result.Usage.AdapterLatencyMS = &response.Calls, response.Retries, response.LatencyMS
	result.Usage.InputTokens, result.Usage.OutputTokens = response.InputTokens, response.OutputTokens
	if !validCount(response.InputTokens, 8192) || !validCount(response.OutputTokens, MaxOutputTokens) {
		result.Reason = "usage_out_of_bounds"
		return result, nil
	}
	if response.Status == "abstained" {
		result.Reason = "adapter_abstained"
		return result, nil
	}
	if response.Status != "ok" || response.AdapterVersion != AdapterVersion || response.Calls != 1 {
		return result, nil
	}
	// A float64 map otherwise treats JSON null as zero. Require actual numeric
	// values for every approved score; nullable token usage remains unknown.
	var rawScores map[string]json.RawMessage
	result.Reason = "malformed_scores"
	if json.Unmarshal(fields["scores"], &rawScores) != nil || len(rawScores) != len(request.Choices) {
		return result, nil
	}
	for _, choice := range request.Choices {
		raw, ok := rawScores[choice.ID]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return result, nil
		}
	}
	after, e := runner.inspect(ctx, request.Model)
	if e != nil || after != result.ModelDigest {
		result.Reason = "model_snapshot_changed_or_unavailable"
		return result, nil
	}
	decide(request, &result, response.Scores)
	return result, nil
}

func validCount(count *int64, max int64) bool { return count == nil || (*count >= 0 && *count <= max) }
func localModelDigest(ctx context.Context, model string) (string, error) {
	provider, err := inference.NewOllamaProviderContext(ctx, OllamaURL, model)
	if err != nil {
		return "", err
	}
	return provider.ModelDigest(), nil
}

func runBridge(ctx context.Context, python string, input []byte) ([]byte, error) {
	home, err := os.MkdirTemp("", "questlock-system-one-")
	if err != nil {
		return nil, errors.New("cannot isolate adapter")
	}
	defer os.RemoveAll(home)
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, errors.New("cannot resolve adapter isolation")
	}
	cmd := exec.CommandContext(ctx, python, "-I", "-B", "-c", bridge)
	cmd.Dir, cmd.Stdin, cmd.Stderr = home, bytes.NewReader(input), io.Discard
	cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Dir(python)}
	var output limitedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, errors.New("isolated adapter failed")
	}
	return output.Bytes(), nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 16384 {
		return 0, errors.New("adapter output exceeds bound")
	}
	return b.Buffer.Write(data)
}
