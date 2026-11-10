package semantic

import "testing"

func TestProseContractIndependentStructuredSyntax(t *testing.T) {
	for _, question := range []string{
		"Return the assessment in plain prose.",
		"Write a single paragraph about the assessment.",
		"Explain the assessment in one short paragraph.",
		"Give me ordinary prose for this assessment.",
		"The response must be a plain paragraph.",
	} {
		for _, answer := range []string{`{"ready":false}`, `["blocked"]`, "```json\n{\"ready\":false}\n```", "| gate | owner |\n| --- | --- |\n| review | Sam |", "gate,owner\nreview,Sam", "gate\towner\nreview\tSam", "<table><tr><td>blocked</td></tr></table>"} {
			if got := CheckReuseAnswerContract(question, answer); len(got) == 0 {
				t.Fatalf("structured answer passed explicit prose request: %q / %q", question, answer)
			}
		}
		if got := CheckReuseAnswerContract(question, "The assessment is blocked by the outstanding review, which Sam owns."); len(got) != 0 {
			t.Fatalf("ordinary prose rejected: %q %v", question, got)
		}
	}
}

func TestNoJSONContractIndependentDirectiveScope(t *testing.T) {
	for _, question := range []string{
		"No JSON. Explain the status.",
		"Explain the status, no JSON.",
		"Return the status without JSON.",
		"Do not return JSON. Explain the status.",
		"Never output JSON.",
		"Return one plain paragraph; no JSON.",
		"Explain the recorded value without JSON.",
		"Summarize the result without JSON.",
		"Summarise the outcome without any JSON.",
		"Describe the state without JSON.",
		"Compose the update without JSON.",
		"Render the explanation without JSON.",
		"Draft the reply without JSON.",
	} {
		for _, answer := range []string{`{"status":"pending"}`, `["pending"]`, "```json\n[\"pending\"]\n```"} {
			if got := CheckReuseAnswerContract(question, answer); len(got) == 0 {
				t.Fatalf("JSON passed an explicit prohibition: %q / %q", question, answer)
			}
		}
	}
	for _, question := range []string{
		"Explain whether JSON would be useful.",
		"Explain why no JSON exports were available.",
		"Summarize the note about no JSON exports.",
		`Explain the phrase "no JSON".`,
		"Explain the phrase 'Return as prose'.",
		"Explain the instruction “do not return JSON”.",
		"The source says no JSON exports were produced. Return only JSON.",
		"Give paragraph identifiers in JSON.",
		"Return details from the record in paragraph 3.",
	} {
		if got := CheckReuseAnswerContract(question, `{"status":"pending"}`); len(got) != 0 {
			t.Fatalf("mention or quotation became a prohibition: %q %v", question, got)
		}
	}
	if got := CheckReuseAnswerContract("Return the answer in prose.", "The text mentions JSON and the literal tag <table>, but is a paragraph."); len(got) != 0 {
		t.Fatalf("mere syntax mention became a table: %v", got)
	}
	if got := CheckReuseAnswerContract("Return the answer in a paragraph.", "Use a | b for the condition.\nThis is the continuation of the same sentence."); len(got) != 0 {
		t.Fatalf("non-table pipe text rejected: %v", got)
	}
}
