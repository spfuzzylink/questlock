package semantic

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func validReuseConfig() ReuseConfig {
	c := validConfig()
	c.Mode = "controlled"
	c.Bindings = map[string]string{"order": "ORD-17"}
	return ReuseConfig{Config: c, ReadOnly: true, Intent: "explain-refund-policy", VerifierModel: "fixture:local"}
}

func TestReuseConfigRequiresExplicitBoundedLocalPolicy(t *testing.T) {
	c := validReuseConfig()
	got, err := NormalizeReuseConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "controlled" || !got.ReadOnly || got.TimeoutMS != MaxTimeoutMS || got.MinSimilarity != DefaultMinSimilarity || got.VerifierMaxInputTokens != DefaultVerifierMaxInputTokens || got.VerifierMaxOutputTokens != DefaultVerifierMaxOutputTokens || got.VerifierTimeoutMS != DefaultVerifierTimeoutMS {
		t.Fatalf("incorrect normalized policy: %+v", got)
	}
	c.Bindings["order"] = "ORD-18"
	if got.Bindings["order"] != "ORD-17" {
		t.Fatal("normalized policy aliases caller bindings")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields["mode"] == nil || fields["intent"] == nil || fields["read_only"] == nil || fields["Config"] != nil {
		t.Fatalf("reuse configuration JSON is not flat: %s", encoded)
	}
	var decoded ReuseConfig
	if json.Unmarshal(encoded, &decoded) != nil || !reflect.DeepEqual(got, decoded) {
		t.Fatal("flat reuse configuration did not round-trip")
	}
	if _, err := NormalizeConfig(got.Config); err == nil {
		t.Fatal("controlled policy silently enabled shadow serving")
	}
	cases := map[string]func(*ReuseConfig){
		"missing mode":        func(c *ReuseConfig) { c.Mode = "" },
		"shadow mode":         func(c *ReuseConfig) { c.Mode = "shadow" },
		"serve mode":          func(c *ReuseConfig) { c.Mode = "serve" },
		"not read only":       func(c *ReuseConfig) { c.ReadOnly = false },
		"missing intent":      func(c *ReuseConfig) { c.Intent = "" },
		"long intent":         func(c *ReuseConfig) { c.Intent = strings.Repeat("x", 129) },
		"padded intent":       func(c *ReuseConfig) { c.Intent = " refund" },
		"control intent":      func(c *ReuseConfig) { c.Intent = "refund\npolicy" },
		"invalid intent":      func(c *ReuseConfig) { c.Intent = string([]byte{0xff}) },
		"missing model":       func(c *ReuseConfig) { c.VerifierModel = "" },
		"cloud model":         func(c *ReuseConfig) { c.VerifierModel = "fixture:cloud" },
		"endpoint model":      func(c *ReuseConfig) { c.VerifierModel = "https://example.invalid/model" },
		"negative input":      func(c *ReuseConfig) { c.VerifierMaxInputTokens = -1 },
		"large input":         func(c *ReuseConfig) { c.VerifierMaxInputTokens = MaxVerifierInputTokens + 1 },
		"negative output":     func(c *ReuseConfig) { c.VerifierMaxOutputTokens = -1 },
		"large output":        func(c *ReuseConfig) { c.VerifierMaxOutputTokens = MaxVerifierOutputTokens + 1 },
		"negative timeout":    func(c *ReuseConfig) { c.VerifierTimeoutMS = -1 },
		"large timeout":       func(c *ReuseConfig) { c.VerifierTimeoutMS = MaxVerifierTimeoutMS + 1 },
		"negative wait":       func(c *ReuseConfig) { c.WaitForCandidateMS = -1 },
		"large wait":          func(c *ReuseConfig) { c.WaitForCandidateMS = MaxWaitForCandidateMS + 1 },
		"invalid base config": func(c *ReuseConfig) { c.TopK = MaxTopK + 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := validReuseConfig()
			change(&c)
			if got, err := NormalizeReuseConfig(c); err == nil || !reflect.DeepEqual(got, ReuseConfig{}) {
				t.Fatalf("invalid policy accepted or partially returned: %+v, %v", got, err)
			}
		})
	}
	for _, limits := range []int64{1, 15} {
		c := validReuseConfig()
		c.VerifierMaxOutputTokens = limits
		if _, err := NormalizeReuseConfig(c); err == nil {
			t.Fatalf("output limit below inference minimum accepted: %d", limits)
		}
	}
	for _, limits := range []int64{16, MaxVerifierOutputTokens} {
		c := validReuseConfig()
		c.VerifierMaxInputTokens = limits
		c.VerifierMaxOutputTokens = limits
		c.VerifierTimeoutMS = int(limits)
		if _, err := NormalizeReuseConfig(c); err != nil {
			t.Fatalf("valid explicit limits rejected: %v", err)
		}
	}
	c = validReuseConfig()
	c.VerifierMaxInputTokens, c.VerifierTimeoutMS = MaxVerifierInputTokens, MaxVerifierTimeoutMS
	c.WaitForCandidateMS = MaxWaitForCandidateMS
	if _, err := NormalizeReuseConfig(c); err != nil {
		t.Fatalf("inclusive maximum limits rejected: %v", err)
	}
}

const acceptingVerdict = `{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`

func TestParseVerdictAcceptsOnlyExplicitDecisionFields(t *testing.T) {
	got, err := ParseVerdict([]byte(acceptingVerdict))
	if err != nil || !got.Accepted() || got.InputTokens != nil || got.OutputTokens != nil || got.Reused || got.DurationMS != 0 || got.ReceiptKey != "" {
		t.Fatalf("valid decision was lost or invented execution evidence: %+v, %v", got, err)
	}
	for _, verdict := range []string{"reject", "unknown"} {
		got, err := ParseVerdict([]byte(strings.Replace(acceptingVerdict, "accept", verdict, 1)))
		if err != nil || got.Accepted() || got.Verdict != verdict {
			t.Fatalf("nonaccepting verdict changed: %+v, %v", got, err)
		}
	}
	for _, name := range []string{"same_intent", "same_facts", "same_constraints", "answer_supported"} {
		got, err := ParseVerdict([]byte(strings.Replace(acceptingVerdict, `"`+name+`":true`, `"`+name+`":false`, 1)))
		if err != nil || got.Accepted() {
			t.Fatalf("negative %s accepted: %+v, %v", name, got, err)
		}
	}
	data := " \n" + acceptingVerdict + "\t\r\n"
	if got, err := ParseVerdict([]byte(data)); err != nil || !got.Accepted() {
		t.Fatal("valid JSON whitespace rejected")
	}
}

func TestParseVerdictRejectsAmbiguousOrModelAuthoredExecutionEvidence(t *testing.T) {
	with := func(extra string) string { return strings.TrimSuffix(acceptingVerdict, "}") + "," + extra + "}" }
	cases := map[string]string{
		"empty": "", "empty object": "{}", "null": "null", "array": "[]", "string": `"accept"`,
		"missing verdict":             strings.Replace(acceptingVerdict, `"verdict":"accept",`, "", 1),
		"missing bool":                strings.Replace(acceptingVerdict, `,"answer_supported":true`, "", 1),
		"null bool":                   strings.Replace(acceptingVerdict, `"same_facts":true`, `"same_facts":null`, 1),
		"null verdict":                strings.Replace(acceptingVerdict, `"accept"`, `null`, 1),
		"number bool":                 strings.Replace(acceptingVerdict, `"same_intent":true`, `"same_intent":1`, 1),
		"string bool":                 strings.Replace(acceptingVerdict, `"same_intent":true`, `"same_intent":"true"`, 1),
		"unknown verdict":             strings.Replace(acceptingVerdict, `"accept"`, `"approved"`, 1),
		"padded verdict":              strings.Replace(acceptingVerdict, `"accept"`, `" accept"`, 1),
		"case verdict":                strings.Replace(acceptingVerdict, `"accept"`, `"ACCEPT"`, 1),
		"duplicate":                   with(`"same_facts":false`),
		"escaped duplicate":           with(`"same_\u0066acts":false`),
		"duplicate replacing missing": strings.Replace(acceptingVerdict, `"same_facts"`, `"same_intent"`, 1),
		"unknown":                     with(`"confidence":1`),
		"input usage":                 with(`"input_tokens":0`),
		"output usage":                with(`"output_tokens":0`),
		"duration":                    with(`"duration_ms":0`),
		"receipt":                     with(`"receipt_key":"forged"`),
		"reuse":                       with(`"reused":true`),
		"trailing object":             acceptingVerdict + `{}`,
		"trailing value":              acceptingVerdict + `true`,
		"trailing junk":               acceptingVerdict + " rubbish",
		"fence":                       "```json\n" + acceptingVerdict + "\n```",
		"deep known value":            strings.Replace(acceptingVerdict, `"same_facts":true`, `"same_facts":`+strings.Repeat("[", 100)+`true`+strings.Repeat("]", 100), 1),
		"nested unknown":              with(`"metadata":{"same_facts":true}`),
		"large":                       strings.Repeat(" ", MaxVerdictBytes) + acceptingVerdict,
		"invalid utf8":                acceptingVerdict + string([]byte{0xff}),
		"broken escape":               strings.Replace(acceptingVerdict, "accept", `acc\xept`, 1),
		"unclosed":                    strings.TrimSuffix(acceptingVerdict, "}"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseVerdict([]byte(data))
			if err == nil || !reflect.DeepEqual(got, ValidityResult{}) {
				t.Fatalf("invalid verdict accepted or partially exposed: %+v, %v", got, err)
			}
		})
	}
}

func TestProtectedTermsRejectChangedFactsAndConstraints(t *testing.T) {
	cases := []struct{ name, original, current, reason string }{
		{"integer", "refund within 30 days", "refund within 31 days", "protected_numbers_changed"},
		{"numeric date", "deadline 2030-04-03", "deadline 2030-03-04", "protected_numbers_changed"},
		{"month", "deadline March 3", "deadline April 3", "protected_numbers_changed"},
		{"ordinal", "use the first option", "use the second option", "protected_numbers_changed"},
		{"numeric sign", "balance -5", "balance 5", "protected_numbers_changed"},
		{"unicode sign", "balance −5", "balance 5", "protected_numbers_changed"},
		{"decimal", "dose .5 mg", "dose .6 mg", "protected_numbers_changed"},
		{"exponent", "limit 1e3", "limit 1e4", "protected_numbers_changed"},
		{"unicode digits", "ticket ١٧", "ticket ١٨", "protected_numbers_changed"},
		{"quantity count", "copy 5 then 5", "copy 5", "protected_numbers_changed"},
		{"quantity order", "copy 5 then 6", "copy 6 then 5", "protected_numbers_changed"},
		{"identifier", "refund order ORD-17", "refund order ORD-18", "protected_identifiers_changed"},
		{"name", "owner Alice", "owner Bob", "protected_identifiers_changed"},
		{"identifier case", "enable MFA", "enable mfa", "protected_identifiers_changed"},
		{"path", "open /api/reset", "open /api/delete", "protected_identifiers_changed"},
		{"email", "send to first@example.com", "send to second@example.com", "protected_identifiers_changed"},
		{"url", "visit https://example.invalid/reset", "visit https://example.invalid/delete", "protected_urls_changed"},
		{"url query", "visit https://example.invalid/?a=1", "visit https://example.invalid/?a=2", "protected_urls_changed"},
		{"double quotes", `return "allow"`, `return "deny"`, "protected_quotes_changed"},
		{"single quotes", "return 'allow'", "return 'deny'", "protected_quotes_changed"},
		{"smart quotes", "return “allow”", "return “deny”", "protected_quotes_changed"},
		{"smart single quotes", "return ‘allow’", "return ‘deny’", "protected_quotes_changed"},
		{"guillemets", "return «alpha»", "return «beta»", "protected_quotes_changed"},
		{"cjk quotes", "return 「alpha」", "return 「beta」", "protected_quotes_changed"},
		{"fullwidth quotes", "return ＂alpha＂", "return ＂beta＂", "protected_quotes_changed"},
		{"code", "return `allow`", "return `deny`", "protected_quotes_changed"},
		{"negation", "do not approve", "do approve", "protected_negations_changed"},
		{"contraction", "don't approve", "approve", "protected_negations_changed"},
		{"smart contraction", "don’t approve", "approve", "protected_negations_changed"},
		{"operator", "amount < 10", "amount <= 10", "protected_operators_changed"},
		{"unicode operator", "amount ≤ 10", "amount < 10", "protected_operators_changed"},
		{"dose", "dose 5 mg", "dose 5 g", "protected_units_changed"},
		{"attached dose", "dose 5mg", "dose 5g", "protected_units_changed"},
		{"duration", "wait 5 ms", "wait 5 s", "protected_units_changed"},
		{"data", "transfer 5 MB", "transfer 5 Mb", "protected_units_changed"},
		{"currency", "refund $5", "refund €5", "protected_units_changed"},
		{"percent", "rate 5%", "rate 5", "protected_units_changed"},
		{"temperature", "limit 5 °C", "limit 5 °F", "protected_units_changed"},
		{"imperial dose", "weight 5 oz", "weight 5 lb", "protected_units_changed"},
		{"written dose", "dose 5 milligrams", "dose 5 grams", "protected_units_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reasons := CheckProtectedTerms(tc.original, tc.current)
			if !slices.Contains(reasons, tc.reason) {
				t.Fatalf("changed protected fact was missed: %q -> %q: %v, want %s", tc.original, tc.current, reasons, tc.reason)
			}
			if !slices.Equal(reasons, CheckProtectedTerms(tc.original, tc.current)) {
				t.Fatal("guard reason ordering is nondeterministic")
			}
		})
	}
}

func TestProtectedTermsVersionTwoAvoidsGrammarAsUnitsAndIdentifiers(t *testing.T) {
	if ProtectedTermsVersion != 2 {
		t.Fatal("update the declared guard version when changing lexical policy")
	}
	for _, pair := range [][2]string{
		{"Enumerate the repair procedure in a short memo.", "Outline a brief repair memo."},
		{"Review a procedure in the manual.", "Describe the procedure from our manual."},
		{"Explain the router setup, including cable checks.", "Explain router setup and cable checks."},
		{"Before drawing conclusions, describe the route.", "Describe the route before any conclusions."},
		{"Summarize the Cedar project.", "Provide a project summary."},
		{"Summarize the Cedar project.", "Give a summary of the Cedar project."},
		{"Describe the voltage of 5 V.", "Outline the voltage: 5V."},
		{"Give a two-line summary of JOB-42", "Provide a two line summary of JOB-42"},
		{"Give a two-line summary of JOB-42", "Provide two line summary of JOB-42"},
		{"Summarize JOB-42's progress", "Give a progress summary for JOB-42"},
		{"Explain JOB-42’s progress", "Describe progress for JOB-42"},
		{"There may be an outline in the manual.", "Provide the manual's outline."},
		{"March through the process.", "Advance through the process."},
		{"Wait for a two-hour period", "Wait for a two hour period"},
	} {
		if reasons := CheckProtectedTerms(pair[0], pair[1]); len(reasons) != 0 {
			t.Fatalf("ordinary grammar blocked the mandatory verifier: %q -> %q: %v", pair[0], pair[1], reasons)
		}
	}
}

func TestProtectedTermsVersionTwoPreservesExplicitChangedFacts(t *testing.T) {
	for _, tc := range []struct{ original, current, reason string }{
		{"Check owner Cedar", "Check owner Maple", "protected_identifiers_changed"},
		{"Describe API behavior", "Describe CLI behavior", "protected_identifiers_changed"},
		{"Review job code ABC-DEF", "Review job code ABC-XYZ", "protected_identifiers_changed"},
		{"Explain JOB-42's progress", "Explain JOB-43 progress", "protected_identifiers_changed"},
		{"Give a two-line report", "Give a three line report", "protected_numbers_changed"},
		{"Use thirty-five samples", "Use thirty-six samples", "protected_numbers_changed"},
		{"Wait for a two-hour delay", "Wait for a three hour delay", "protected_numbers_changed"},
		{"Record 7mg", "Record 7Mg", "protected_units_changed"},
		{"Transfer 7MB", "Transfer 7Mb", "protected_units_changed"},
		{"Cost USD 7", "Cost EUR 7", "protected_units_changed"},
		{"Batch size < 7", "Batch size <= 7", "protected_operators_changed"},
	} {
		if reasons := CheckProtectedTerms(tc.original, tc.current); !slices.Contains(reasons, tc.reason) {
			t.Fatalf("explicit changed fact reached the verifier: %q -> %q: %v, want %s", tc.original, tc.current, reasons, tc.reason)
		}
	}
}

func TestProtectedTermsNaturalLanguageStillRequiresIndependentValidity(t *testing.T) {
	// Different natural-language constraints can produce the same lexical
	// snapshot. Only the mandatory evidence-based verifier may authorize reuse;
	// this guard deliberately makes no free-form equivalence claim.
	for _, pair := range [][2]string{
		{"at most 10", "at least 10"},
		{"within 10 days", "after 10 days"},
		{"allow access", "deny access"},
		{"all accounts", "some accounts"},
		{"required field", "optional field"},
	} {
		if reasons := CheckProtectedTerms(pair[0], pair[1]); len(reasons) != 0 {
			t.Fatalf("natural-language constraint was treated as a symbolic operator: %v", reasons)
		}
	}
	// Negation remains a conservative lexical guard: this synonymous pair can
	// be rejected. Recognizing arbitrary paraphrases is the verifier's job.
	if reasons := CheckProtectedTerms("unused labels", "labels not used"); !slices.Contains(reasons, "protected_negations_changed") {
		t.Fatal("conservative negation policy changed without a separate decision")
	}
}

func TestProtectedTermsAcceptHarmlessWordingButDoNotProveEquivalence(t *testing.T) {
	for _, pair := range [][2]string{
		{"How do I reset my password?", "Please explain password reset."},
		{"Where is the wiki access link?", "How do I open the wiki?"},
		{"Summarize incident INC-17 for 2 hours", "Please give incident INC-17 summary for 2 hours"},
		{"Explain refund order ORD-17", "What is the refund policy for order ORD-17?"},
		{`Please return "reset_password" for OPS-17`, `Give "reset_password" for OPS-17 please`},
		{"Don't approve order ORD-17", "Please don't approve order ORD-17"},
		{"Transfer 5mg", "Transfer 5mg"},
		{"Which password reset steps apply?", "Which password reset steps apply?"},
	} {
		if reasons := CheckProtectedTerms(pair[0], pair[1]); len(reasons) != 0 {
			t.Fatalf("unchanged protected terms rejected: %q -> %q: %v", pair[0], pair[1], reasons)
		}
	}
	// Purely lexical guards cannot establish intent, facts or relationships in
	// arbitrary prose. This pair requires the independent validity verifier.
	if reasons := CheckProtectedTerms("owner alice", "owner bob"); len(reasons) != 0 {
		t.Fatalf("test no longer illustrates the documented lexical limitation: %v", reasons)
	}
}

func TestProtectedTermsFailClosedOnUnsupportedOrMalformedText(t *testing.T) {
	for _, text := range []string{"", " \n\t", strings.Repeat("x", MaxInputBytes+1), string([]byte{0xff}), "refund\x00order", "refund\u202eorder", "refund\u200border"} {
		for _, pair := range [][2]string{{text, "refund order"}, {"refund order", text}} {
			if reasons := CheckProtectedTerms(pair[0], pair[1]); !slices.Contains(reasons, "invalid_protected_text") {
				t.Fatalf("invalid text admitted: %v", reasons)
			}
		}
	}
	for _, text := range []string{`return "allow`, "return 'allow", "return `allow", "return “allow", "return ”allow”"} {
		if reasons := CheckProtectedTerms(text, text); !slices.Contains(reasons, "invalid_protected_quotes") {
			t.Fatalf("malformed quotes admitted: %q: %v", text, reasons)
		}
	}
	if reasons := CheckProtectedTerms(strings.Repeat("x", MaxInputBytes), strings.Repeat("x", MaxInputBytes)); len(reasons) != 0 {
		t.Fatalf("maximum bounded text rejected: %v", reasons)
	}
}
