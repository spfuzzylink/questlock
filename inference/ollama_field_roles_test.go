package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func fieldRoleTypesFixture() map[string]string {
	return map[string]string{"units": "number", "action_code": "string", "record_id": "string"}
}

func fieldRolesFixture() map[string]string {
	return map[string]string{"units": "quantity", "action_code": "policy_action", "record_id": "identifier"}
}

func TestOllamaFieldRolesNilPreservesTypedIdentityAndSchema(t *testing.T) {
	s := ollamaOptionsSpec()
	s.Ollama = &OllamaOptions{Format: "json", PromptMode: "bounded_text", OutputTypes: map[string]string{"units": "integer", "handling": "string", "record_id": "string"}}
	before, err := BuildPlan(s)
	if err != nil || before.Fingerprint != "fd772dc0cccbf1e08b3c2951a762acba3648021c0f1236019b2f6ddd19012478" {
		t.Fatalf("nil field roles changed the prior typed-contract identity: %s, %v", before.Fingerprint, err)
	}
	schemaBefore, _ := ollamaOutputTypesSchema(s.Ollama)
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded), "field_roles") {
		t.Fatal("nil field roles changed existing JSON bytes")
	}
	s.Ollama.FieldRoles = map[string]string{"units": "quantity", "handling": "policy_action", "record_id": "identifier"}
	after, err := BuildPlan(s)
	schemaAfter, _ := ollamaOutputTypesSchema(s.Ollama)
	if err != nil || before.Fingerprint == after.Fingerprint || before.Request.Prefix != after.Request.Prefix || before.Request.Input != after.Request.Input || string(schemaBefore) != string(schemaAfter) {
		t.Fatal("manual roles failed to pin identity or changed user source/schema", err)
	}
	s.Ollama.FieldRoles = map[string]string{"record_id": "identifier", "handling": "policy_action", "units": "quantity"}
	reordered, err := BuildPlan(s)
	if err != nil || reordered.Fingerprint != after.Fingerprint {
		t.Fatal("manual map insertion order changed identity", err)
	}
}

func TestOllamaFieldRolesPreserveSchemaPromptAndCountActualSystem(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"", "bounded_text"} {
			t.Run(strconv.FormatBool(stream)+"/"+mode, func(t *testing.T) {
				const text = `{"units":2.7500,"action_code":"009","record_id":"00042"}`
				fixture := mutatedFixture(t, ollamaGenerateFixture, func(frame map[string]any) { frame["response"] = text })
				recorded := make(chan map[string]any, 1)
				p, calls := ollamaStreamProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					recorded <- body
					if stream {
						w.Header().Set("Content-Type", "application/x-ndjson")
						writeStreamFrame(w, fixture)
					} else {
						io.WriteString(w, fixture)
					}
				}, nil)
				req := ollamaTestRequest()
				req.Stream, req.ContextWindow = stream, 8192
				req.Ollama = &OllamaOptions{Format: "json", PromptMode: mode, OutputTypes: fieldRoleTypesFixture()}
				baseRendering, err := renderOllamaRequest(req, "Model default instructions.")
				if err != nil {
					t.Fatal(err)
				}
				baseCount, err := p.CountInputTokens(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				req.Ollama.FieldRoles = fieldRolesFixture()
				before, _ := json.Marshal(req)
				count, err := p.CountInputTokens(context.Background(), req)
				if err != nil || calls.generate.Load() != 0 {
					t.Fatal("manual field-role preflight failed or generated", err)
				}
				got, err := p.Generate(context.Background(), req)
				if err != nil || !got.Complete || got.Text != text || calls.generate.Load() != 1 {
					t.Fatalf("roles rewrote numeric codes/quantity, rejected shape or retried: %+v, %v", got, err)
				}
				roles, _ := json.Marshal(req.Ollama.FieldRoles)
				wantSystem := baseRendering.system + "\n\nField roles:\n" + string(roles) + "\n\n" + ollamaFieldRoleDefinitions
				body := <-recorded
				var schema any
				schemaBytes, _ := ollamaOutputTypesSchema(req.Ollama)
				json.Unmarshal(schemaBytes, &schema)
				want := map[string]any{"model": req.Model, "system": wantSystem, "prompt": baseRendering.prompt, "stream": stream, "think": false, "format": schema, "options": map[string]any{"num_predict": float64(req.MaxOutputTokens), "num_ctx": float64(req.ContextWindow)}}
				if !reflect.DeepEqual(body, want) || count-baseCount != int64(len(wantSystem)-len(baseRendering.system)) {
					t.Fatalf("manual roles altered schema/user prompt/defaults or escaped context accounting: %+v", body)
				}
				after, _ := json.Marshal(req)
				if string(before) != string(after) {
					t.Fatal("manual role rendering changed the caller request")
				}
			})
		}
	}
}

func TestOllamaFieldRolesRejectInvalidDeclarationsBeforeHTTP(t *testing.T) {
	tooMany := map[string]string{}
	for i := range 33 {
		tooMany["field_"+strconv.Itoa(i)] = "identifier"
	}
	cases := []OllamaOptions{
		{Format: "json", FieldRoles: fieldRolesFixture()},
		{OutputTypes: fieldRoleTypesFixture(), FieldRoles: fieldRolesFixture()},
		{Format: "json", OutputTypes: fieldRoleTypesFixture(), FieldRoles: map[string]string{}},
		{Format: "json", OutputTypes: fieldRoleTypesFixture(), FieldRoles: tooMany},
		{Format: "json", OutputTypes: fieldRoleTypesFixture(), FieldRoles: map[string]string{"missing": "quantity"}},
	}
	for _, key := range []string{"Action_code", "_units", "units_", "units__value", "é", strings.Repeat("a", 65)} {
		cases = append(cases, OllamaOptions{Format: "json", OutputTypes: fieldRoleTypesFixture(), FieldRoles: map[string]string{key: "quantity"}})
	}
	for _, role := range []string{"action", "count", "expected=009", "policy_action:009", "identifier ", "quantity|integer"} {
		cases = append(cases, OllamaOptions{Format: "json", OutputTypes: fieldRoleTypesFixture(), FieldRoles: map[string]string{"units": role}})
	}
	for _, entry := range []struct{ typ, role string }{{"string", "quantity"}, {"boolean", "quantity"}, {"integer", "policy_action"}, {"number", "policy_action"}, {"boolean", "identifier"}, {"number", "identifier"}} {
		cases = append(cases, OllamaOptions{Format: "json", OutputTypes: map[string]string{"field": entry.typ}, FieldRoles: map[string]string{"field": entry.role}})
	}
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	for _, options := range cases {
		s := ollamaOptionsSpec()
		s.Ollama = &options
		if _, err := BuildPlan(s); err == nil {
			t.Fatal("invalid manual field role was planned", options)
		}
		for _, stream := range []bool{false, true} {
			req := ollamaTestRequest()
			req.Stream, req.Ollama = stream, &options
			if _, err := p.CountInputTokens(context.Background(), req); err == nil {
				t.Fatal("invalid manual field role counted", options)
			}
			if _, err := p.Generate(context.Background(), req); err == nil {
				t.Fatal("invalid manual field role generated", options)
			}
		}
	}
	if calls.tags.Load() != 1 || calls.show.Load() != 1 || calls.generate.Load() != 0 {
		t.Fatal("invalid manual roles reached HTTP after constructor discovery")
	}
}

func TestOllamaFieldRolesAcceptBoundedSubsetsAndQuantityTypes(t *testing.T) {
	for _, typ := range []string{"integer", "number"} {
		if err := validateOllamaOptions(&OllamaOptions{Format: "json", OutputTypes: map[string]string{"amount": typ, "note": "string"}, FieldRoles: map[string]string{"amount": "quantity"}}); err != nil {
			t.Fatal("valid typed subset rejected", err)
		}
	}
	types, roles := map[string]string{}, map[string]string{}
	for i := range 32 {
		types["id_"+strconv.Itoa(i)] = "string"
		roles["id_"+strconv.Itoa(i)] = "identifier"
	}
	if err := validateOllamaOptions(&OllamaOptions{Format: "json", OutputTypes: types, FieldRoles: roles}); err != nil {
		t.Fatal("bounded 32-role declaration rejected", err)
	}
}

func TestOllamaFieldRolesCountHintsAgainstContextAndPlannedBytes(t *testing.T) {
	p, calls := ollamaFixtureProvider(t, ollamaGenerateFixture)
	req := ollamaTestRequest()
	req.ContextWindow = 8192
	req.Ollama = &OllamaOptions{Format: "json", OutputTypes: fieldRoleTypesFixture()}
	baseCount, err := p.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ContextWindow = baseCount + req.MaxOutputTokens
	req.Ollama.FieldRoles = fieldRolesFixture()
	if _, err := p.CountInputTokens(context.Background(), req); err == nil {
		t.Fatal("role guidance bytes escaped context cap")
	}
	if _, err := p.Generate(context.Background(), req); err == nil || calls.generate.Load() != 0 {
		t.Fatal("role guidance context overrun reached generation")
	}
	s := ollamaOptionsSpec()
	s.Ollama = &OllamaOptions{Format: "json", OutputTypes: fieldRoleTypesFixture()}
	plan, err := BuildPlan(s)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := renderOllamaRequest(plan.Request, "")
	s.Input += strings.Repeat("x", (512<<10)-rendered.inputBytes()-1)
	if _, err := BuildPlan(s); err != nil {
		t.Fatal("base contract fixture should fit planned bytes", err)
	}
	s.Ollama.FieldRoles = fieldRolesFixture()
	if _, err := BuildPlan(s); err == nil {
		t.Fatal("role guidance escaped planned byte cap")
	}
}
