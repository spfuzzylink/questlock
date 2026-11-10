package semantic

import (
	"strings"
	"testing"
)

func TestLanguageContractIndependentExplicitChangesAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, original, instructions, current string
		reject                                bool
	}{
		{"no declared language", "Summarize the queue state.", "Use only the evidence.", "Explain that queue state.", false},
		{"instruction default unchanged", "Explain the result.", "Reply in English.", "Summarize the same result.", false},
		{"Spanish change", "Explain the result.", "Reply in English.", "Explain the result in Spanish.", true},
		{"French change", "Explain the result.", "Reply in English.", "Give a French summary of the result.", true},
		{"English change", "Respond in French about the queue.", "Use only the evidence.", "Respond in English about the queue.", true},
		{"original overrides default", "Answer in French about the schedule.", "Reply in English.", "For that schedule, reply in French.", false},
		{"current falls back to instruction", "Answer in French about the schedule.", "Reply in English.", "Explain that schedule.", true},
		{"unknown source explicit target", "Explain the schedule.", "Use approved facts.", "Answer in English about the schedule.", true},
		{"basic language tag", "Answer in Spanish about the schedule.", "Use approved facts.", "Output language: es. Explain the schedule.", false},
		{"basic language tag changed", "Reply in French.", "Use facts.", "Language: en. Explain the schedule.", true},
		{"regional tag uncertain", "Answer in English.", "Use facts.", "Answer in en-GB.", true},
		{"unrecognized explicit tag", "Answer in English.", "Use facts.", "Output language: zz-Latn.", true},
		{"unknown named language", "Answer in English.", "Use facts.", "Respond in German.", true},
		{"same unsupported language remains uncertain", "Answer in German.", "Use facts.", "Reply in German.", true},
		{"unknown explicit label", "Answer in English.", "Use facts.", "Language: madeup.", true},
		{"multiple requested languages", "Answer in English.", "Use facts.", "Respond in English and French.", true},
		{"negative language constraint", "Answer in English.", "Use facts.", "Do not answer in English.", true},
		{"instruction prohibition retained", "Answer in English.", "Do not answer in Spanish.", "Answer in Spanish.", true},
		{"nonconflicting negative constraint", "Answer in English.", "Use facts.", "Do not answer in French.", false},
		{"translation target not input", "Translate from Spanish into French.", "Use approved text.", "Translate from Spanish into French.", false},
		{"translation target changed", "Translate from Spanish into French.", "Use approved text.", "Translate from Spanish into English.", true},
		{"language prefix", "Answer in English.", "Use facts.", "In Spanish, explain the result.", true},
		{"plain language modifier", "Write in plain English about the result.", "Use facts.", "Reply in clear English about it.", false},
		{"declarative output language", "Reply in English.", "Use facts.", "The response must be in French.", true},
		{"Spanish directive", "Answer in English.", "Use facts.", "Responde en español.", true},
		{"French directive", "Answer in English.", "Use facts.", "Réponds en français.", true},
		{"real target after quoted instruction", "Answer in English.", "Use facts.", `Explain "Reply in English" in Spanish.`, true},
		{"translation target after quoted text", "Answer in English.", "Use facts.", `Translate "Reply in Spanish" into French.`, true},
		{"no answer-language heuristic", "What does bonjour mean?", "Use approved facts.", "Explain the greeting's meaning.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckReuseLanguageContract(tc.original, tc.instructions, tc.current)
			if (len(got) > 0) != tc.reject {
				t.Fatalf("rejection=%v want%v codes=%v", len(got) > 0, tc.reject, got)
			}
		})
	}
}

func TestLanguageContractIndependentMentionsAndQuotedDataAreNotDirectives(t *testing.T) {
	for _, current := range []string{
		"Compare the English and Spanish sections of the handbook.",
		`Explain the phrase "Reply in Spanish".`,
		"Explain the phrase 'Reply in French'.",
		"Explain the phrase “Reply in Spanish”.",
		"Explain the phrase ‘Reply in French’.",
		"Explain the phrase `Reply in Spanish`.",
		"> Reply in French\nExplain the quoted instruction.",
		"Explain how to write a reply in French.",
		"Explain the meaning of answer in Spanish.",
		`Explain the phrase "quoted \"Answer in Spanish\" inside data".`,
		"Return the answer in JSON.",
		"Write the answer in detail.",
		"In Kubernetes, explain the namespace.",
		"Return the measurements in Field/Result columns.",
		"Give the values in an Item/Value table.",
		"Return the estimate in USD.",
		"Write an English-style code description.",
		"Explain why English is the default language.",
		"What is the default language: English or French?",
	} {
		t.Run(current, func(t *testing.T) {
			if got := CheckReuseLanguageContract("Explain the handbook.", "Answer in English.", current); len(got) != 0 {
				t.Fatalf("quoted data or subject became a language directive: %v", got)
			}
		})
	}
	if got := CheckReuseLanguageContract("Explain the result.", `The source says "Answer in French"; answer in English.`, "Reply in Spanish."); len(got) == 0 {
		t.Fatal("real current directive was hidden by an unrelated quoted example")
	}
	if got := CheckReuseLanguageContract("Explain the result.", "Reply in English.", `Explain "Reply in English". Reply in Spanish.`); len(got) == 0 {
		t.Fatal("directive after quoted example was ignored")
	}
}

func TestLanguageContractIndependentInstructionDefaultsOnly(t *testing.T) {
	for _, instructions := range []string{"English is the default language.", "Default language: English.", "Use English by default."} {
		if got := CheckReuseLanguageContract("Summarize the status.", instructions, "Respond in Spanish about the status."); len(got) == 0 {
			t.Fatalf("instruction default did not reject language change: %q", instructions)
		}
		if got := CheckReuseLanguageContract("Summarize the status.", instructions, "Respond in English about the status."); len(got) != 0 {
			t.Fatalf("same explicit target lost known instruction default: %q %v", instructions, got)
		}
		if got := CheckReuseLanguageContract("Respond in Spanish about the status.", instructions, "Reply in Spanish about that status."); len(got) != 0 {
			t.Fatalf("source question failed to override instruction default: %q %v", instructions, got)
		}
	}
	for _, instructions := range []string{`Explain the phrase "English is the default language".`, "The handbook says English is the default language."} {
		if got := CheckReuseLanguageContract("Summarize the status.", instructions, "Respond in English about the status."); len(got) != 1 || got[0] != "output_language_source_unknown" {
			t.Fatalf("mention was treated as an instruction default: %q %v", instructions, got)
		}
	}
}

func TestLanguageContractIndependentInputBoundsAndFixedDiagnostics(t *testing.T) {
	for _, args := range [][3]string{
		{"", "Use facts.", "Reply in English."},
		{"Explain.", string([]byte{0xff}), "Reply in English."},
		{"Explain.", "Use facts.", strings.Repeat("x", MaxInputBytes+1)},
		{"Explain.", "Use facts.", "Reply\x00 in English."},
	} {
		if got := CheckReuseLanguageContract(args[0], args[1], args[2]); len(got) != 1 || got[0] != "invalid_language_contract_text" {
			t.Fatalf("invalid input diagnostics=%v", got)
		}
	}
	if got := CheckReuseLanguageContract("Reply in English.", "", "Reply in French."); len(got) != 1 || got[0] != "output_language_changed" {
		t.Fatalf("language change leaked input or lost fixed diagnostic: %v", got)
	}
}
