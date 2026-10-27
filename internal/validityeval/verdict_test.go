package validityeval

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/spfuzzylink/questlock/semantic"
)

const acceptingBatchVerdict = `{"i0":true,"f0":true,"c0":true,"s0":true,"i1":true,"f1":true,"c1":true,"s1":true}`
const mixedBatchVerdict = `{"i0":true,"f0":true,"c0":true,"s0":true,"i1":true,"f1":true,"c1":false,"s1":false}`

func TestBatchVerdictRequiresEachIndependentBooleanDecision(t *testing.T) {
	fields := []string{"i0", "f0", "c0", "s0", "i1", "f1", "c1", "s1"}
	// Exercise every combination: one question's positive factors never fill in
	// missing or false factors for the other question.
	for mask := 0; mask < 256; mask++ {
		values := make(map[string]bool, len(fields))
		for i, field := range fields {
			values[field] = mask&(1<<i) != 0
		}
		data, _ := json.Marshal(values)
		got, err := parseBatchVerdict(data)
		if err != nil || len(got) != 2 {
			t.Fatalf("valid factors %s rejected: %+v %v", data, got, err)
		}
		for i, decision := range got {
			want := semantic.ValidityResult{Verdict: "unknown", SameIntent: values[fields[4*i]], SameFacts: values[fields[4*i+1]], SameConstraints: values[fields[4*i+2]], AnswerSupported: values[fields[4*i+3]]}
			if want.SameIntent && want.SameFacts && want.SameConstraints && want.AnswerSupported {
				want.Verdict = "accept"
			}
			if !reflect.DeepEqual(decision, want) || decision.Accepted() != (want.Verdict == "accept") {
				t.Fatalf("question %d factors were combined, hidden, or authored execution evidence for %s: %+v != %+v", i, data, decision, want)
			}
		}
	}
}

func TestBatchVerdictRejectsMalformedWithoutPartialDecisions(t *testing.T) {
	for _, input := range []string{
		"", `A`, `true`, `"accept"`, `[]`, `{}`, `{"q0":"A","q1":"A"}`, `{"v":["A","A"]}`,
		strings.Replace(acceptingBatchVerdict, `"i0":true,`, "", 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":null`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":"true"`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":1`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":[true]`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":{"v":true}`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":true,"i0":false`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i0":true,"\u00690":true`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"I0":true`, 1),
		strings.Replace(acceptingBatchVerdict, `"i0":true`, `"i00":true`, 1),
		strings.Replace(acceptingBatchVerdict, `"s1":true`, `"s2":true`, 1),
		strings.TrimSuffix(acceptingBatchVerdict, "}") + `,"input_tokens":0}`,
		strings.TrimSuffix(acceptingBatchVerdict, "}") + `,"shared_work":true}`,
		strings.TrimSuffix(acceptingBatchVerdict, "}") + `,"verdict":"accept"}`,
		acceptingBatchVerdict + `{}`, acceptingBatchVerdict + ` false`, acceptingBatchVerdict + ` junk`,
		strings.TrimSuffix(acceptingBatchVerdict, "}") + `,}`, strings.TrimSuffix(acceptingBatchVerdict, "}"),
		"```json\n" + acceptingBatchVerdict + "\n```",
		strings.Repeat(" ", maxBatchVerdictBytes) + acceptingBatchVerdict, string([]byte{0xff}),
	} {
		t.Run(input, func(t *testing.T) {
			got, err := parseBatchVerdict([]byte(input))
			if err == nil || got != nil {
				t.Fatalf("malformed batch exposed partial decisions: %+v %v", got, err)
			}
		})
	}
	boundary := strings.Repeat(" ", maxBatchVerdictBytes-len(acceptingBatchVerdict)) + acceptingBatchVerdict
	if got, err := parseBatchVerdict([]byte(boundary)); err != nil || len(got) != 2 || !got[0].Accepted() || !got[1].Accepted() {
		t.Fatalf("valid byte boundary rejected: %+v %v", got, err)
	}
	if got, err := parseBatchVerdict([]byte("\n\t" + strings.Replace(acceptingBatchVerdict, `"i0"`, `"\u00690"`, 1) + "\n")); err != nil || !got[0].Accepted() {
		t.Fatalf("valid JSON encoding rejected: %+v %v", got, err)
	}
}

func TestSingleVerdictRetainsOriginalPublicContract(t *testing.T) {
	legacy := []byte(`{"verdict":"accept","same_intent":true,"same_facts":true,"same_constraints":true,"answer_supported":true}`)
	if got, err := semantic.ParseVerdict(legacy); err != nil || !got.Accepted() {
		t.Fatalf("original public parser changed: %+v %v", got, err)
	}
	for _, text := range []string{`{"v":"A"}`, acceptingBatchVerdict} {
		if got, err := semantic.ParseVerdict([]byte(text)); err == nil || got.Accepted() {
			t.Fatalf("experimental representation replaced standalone contract: %+v %v", got, err)
		}
	}
}
