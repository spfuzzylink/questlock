package delegation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type independentNativeVerifierBody struct {
	Model     string          `json:"model"`
	System    string          `json:"system"`
	Prompt    string          `json:"prompt"`
	Format    json.RawMessage `json:"format"`
	Stream    bool            `json:"stream"`
	Think     *bool           `json:"think"`
	KeepAlive *int64          `json:"keep_alive"`
	Options   struct {
		Predict     int64   `json:"num_predict"`
		Context     int64   `json:"num_ctx"`
		Temperature float64 `json:"temperature"`
		Seed        *int64  `json:"seed"`
	} `json:"options"`
}

func TestIndependentFactorizedNativeWireSeparatesPolicyAndDataWithBoundedPreflight(t *testing.T) {
	// This fake runtime verifies wire shape and execution limits only. Its
	// scripted verdicts are not evidence of a model's language accuracy.
	const model = "fixture:local"
	const template = "{{ .System }} {{ .Prompt }}"
	const installedSystem = "INSTALLED_SYSTEM_MUST_NOT_REPLACE_VERIFIER_POLICY"
	const dataDirective = "UNTRUSTED_DATA_DIRECTIVE_MUST_NOT_ENTER_SYSTEM"
	const guidance = "The type contract specifies representation only. Derive values from the approved task and supplied input. Preserve copied strings exactly, including numeric-looking codes. Use the task's stated action or policy when a field requests one; do not substitute an unrelated computed quantity. Compute numeric quantities only where requested. Recheck every requested field against its source before returning the object."
	var mu sync.Mutex
	var generated []independentNativeVerifierBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("local verifier sent credentials to the fixture")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"`+model+`","digest":"`+strings.Repeat("a", 64)+`"}]}`)
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"template": template, "system": installedSystem, "capabilities": []string{"completion"}, "model_info": map[string]any{"general.architecture": "fixture", "fixture.context_length": 65536}})
		case "/api/generate":
			var body independentNativeVerifierBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			generated = append(generated, body)
			mu.Unlock()
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(strings.TrimPrefix(body.Prompt, "\n\nTask:\n")), &fields) != nil {
				t.Error("native verifier prompt is not a direct JSON task record")
			}
			text := `{"verdict":"accept","supported":true,"complete":true}`
			if fields["question"] != nil {
				text = `{"verdict":"accept","answer_covers_current":true,"unchanged_facts":true,"same_output_contract":true}`
			}
			json.NewEncoder(w).Encode(map[string]any{"model": model, "response": text, "done": true, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 20})
		default:
			t.Error("unexpected fixture endpoint", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	snapshot := func() []independentNativeVerifierBody {
		mu.Lock()
		defer mu.Unlock()
		return append([]independentNativeVerifierBody(nil), generated...)
	}
	c := controlledTestConfig()
	c.VerifierPolicy, c.VerifierModel = "factorized-v1", model
	c.VerifierMaxInputTokens, c.VerifierMaxOutputTokens = 16384, 128
	broker := testBroker(t)
	v, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	if len(snapshot()) != 0 {
		t.Fatal("runtime discovery performed generation")
	}
	request := factorizedTestRequest()
	request.Context += "\nLiteral task data: " + dataDirective + "."
	first, err := v.Verify(t.Context(), request)
	if err != nil || !first.Accepted() || !first.Durable || len(first.Work) != 2 {
		t.Fatalf("scripted native verifier failed: %+v %v", first, err)
	}
	bodies := snapshot()
	if len(bodies) != 2 {
		t.Fatalf("expected one generation per stage; got %d", len(bodies))
	}
	var sourceEstimate int64
	for i, body := range bodies {
		policy := factorizedSupportInstructions
		types := map[string]string{"verdict": "string", "supported": "boolean", "complete": "boolean"}
		expected := map[string]any{"original_question": request.OriginalQuestion, "instructions": request.Instructions, "context": request.Context, "cached_answer": request.CachedAnswer, "bindings": request.Bindings}
		if i == 1 {
			policy = factorizedPairInstructions
			types = map[string]string{"verdict": "string", "answer_covers_current": "boolean", "unchanged_facts": "boolean", "same_output_contract": "boolean"}
			expected["question"] = request.Question
		}
		declared, _ := json.Marshal(types)
		if !strings.HasPrefix(body.System, policy+"\n\nOutput field types (JSON object):\n"+string(declared)+"\n\n") || !strings.Contains(body.System, guidance) || strings.Contains(body.System, installedSystem) || strings.Contains(body.System, dataDirective) || strings.Contains(body.System, request.Intent) || strings.Contains(body.System, c.Intent) {
			t.Fatal("native system lost policy/type guidance or promoted task metadata/data into instructions")
		}
		if !strings.HasPrefix(body.Prompt, "\n\nTask:\n{") || strings.Contains(body.Prompt, policy) || strings.Contains(body.Prompt, request.Intent) || strings.Contains(body.Prompt, c.Intent) {
			t.Fatal("native prompt gained an outer instruction wrapper or source intent")
		}
		expectedJSON, _ := json.Marshal(expected)
		var actual, want any
		if json.Unmarshal([]byte(strings.TrimPrefix(body.Prompt, "\n\nTask:\n")), &actual) != nil || json.Unmarshal(expectedJSON, &want) != nil || !reflect.DeepEqual(actual, want) {
			t.Fatalf("native stage %d lost or wrapped complete task data: %s", i, body.Prompt)
		}
		var schema struct {
			Type                 string                           `json:"type"`
			Properties           map[string]struct{ Type string } `json:"properties"`
			Required             []string                         `json:"required"`
			AdditionalProperties bool                             `json:"additionalProperties"`
		}
		if json.Unmarshal(body.Format, &schema) != nil || schema.Type != "object" || schema.AdditionalProperties || len(schema.Properties) != len(types) || len(schema.Required) != len(types) {
			t.Fatal("native wire lost its strict primitive output schema")
		}
		for field, typ := range types {
			if schema.Properties[field].Type != typ {
				t.Fatalf("native wire lost declared type for %s", field)
			}
		}
		if body.Model != model || body.Stream || body.Think == nil || *body.Think || body.KeepAlive == nil || *body.KeepAlive != 0 || body.Options.Predict != c.VerifierMaxOutputTokens/2 || body.Options.Context != c.VerifierMaxInputTokens/2+c.VerifierMaxOutputTokens/2+512 || body.Options.Temperature != 0 {
			t.Fatalf("native prompting changed bounded local generation controls: %+v", body)
		}
		if i == 0 {
			sourceEstimate = int64(len(body.System)+len(body.Prompt)+len(body.Format)+len(template)) + 512
		}
	}
	repeated, err := v.Verify(t.Context(), request)
	if err != nil || !repeated.Accepted() || !repeated.Reused || repeated.GenerationCalled || repeated.ReceiptKey != first.ReceiptKey || len(snapshot()) != 2 {
		t.Fatalf("exact native decision repeat generated or changed its receipt: %+v %v", repeated, err)
	}
	// Put the entire native system, schema and task one byte above the source
	// stage input cap. Counting must reject before either stage can generate.
	c.VerifierMaxInputTokens = 2 * (sourceEstimate - 1)
	bounded, err := NewFactorizedVerifier(t.Context(), broker, srv.URL, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bounded.Close)
	blocked, err := bounded.Verify(t.Context(), request)
	if err == nil || blocked.Accepted() || blocked.GenerationCalled || len(snapshot()) != 2 {
		t.Fatalf("native prompt/schema bytes bypassed the stage preflight cap: %+v %v", blocked, err)
	}
}
