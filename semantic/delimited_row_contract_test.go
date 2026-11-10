package semantic

import (
	"strings"
	"testing"
)

func TestDelimitedRowsRequirePositiveDirectionAndProvableWidth(t *testing.T) {
	const prose = "1. Clerk: Inspect the label.\n2. Clerk: File the record.\n3. Reviewer: Approve the record."
	for _, tc := range []struct {
		name, question, answer string
		reject                 bool
	}{
		{"named width rejects numbered prose", "Give the outline with one CSV row per item and columns position, actor, instruction.", prose, true},
		{"named width accepts data without header", "Give the outline with one CSV row per item and columns position, actor, instruction.", "1,clerk,inspect\n2,reviewer,approve", false},
		{"polite direct request", "Could you please provide the outline with one CSV row per item and columns position, actor, instruction?", prose, true},
		{"tsv named width", "Please give the outline with one TSV record per item and columns position, actor, instruction.", "1\tclerk\tinspect\n2\treviewer\tapprove", false},
		{"tsv rejects csv", "Give the outline with one TSV row per item and columns position, actor, instruction.", "1,clerk,inspect", true},
		{"one column is valid", "Give the notes with one CSV row per item and columns text.", "first note\nsecond note", false},
		{"unproved width does not prohibit one column", "Give the notes with one CSV row per item.", prose, false},
		{"quoted comma and newline", "Give the outline with one CSV row per item and columns position, actor, instruction.", "1,clerk,\"inspect, then file\"\n2,reviewer,\"check\nthe record\"", false},
		{"bad quoting", "Give the outline with one CSV row per item and columns position, actor, instruction.", "1,clerk,\"inspect", true},
		{"numeric named conflict", "Give the outline with one CSV row per item, exactly two columns and columns position, actor, instruction.", "1,clerk,inspect", true},
		{"legacy csv named width", "Return the outline as CSV with columns position, actor, instruction.", prose, true},
		{"explicit header rejects data first", "Give the outline with one CSV row per item and header columns position, actor, instruction.", "1,clerk,inspect", true},
		{"explicit header matches", "Give the outline with one CSV row per item and header columns position, actor, instruction.", "position,actor,instruction\n1,clerk,inspect", false},
		{"negated header stays data", "Give the outline with one CSV row per item and do not include header columns position, actor, instruction.", "1,clerk,inspect", false},
		{"negated named constraint defers width", "Give the outline with one CSV row per item but not columns position, actor, instruction.", prose, false},
		{"same width header order differs", "Give the outline with one CSV row per item and header columns position, actor, instruction.", "actor,position,instruction\nclerk,1,inspect", true},
		{"proposition question", "Would an outline with one CSV row per item and columns position, actor, instruction be useful?", prose, false},
		{"boolean about output", "Can the outline have one CSV row per item and columns position, actor, instruction?", prose, false},
		{"negation", "Give the outline not with one CSV row per item and columns position, actor, instruction.", prose, false},
		{"hypothetical", "Give an outline if requested with one CSV row per item and columns position, actor, instruction.", prose, false},
		{"quoted command", `Explain "Give the outline with one CSV row per item and columns position, actor, instruction".`, prose, false},
		{"code command", "Explain this:\n```text\nGive the outline with one CSV row per item and columns position, actor, instruction.\n```", prose, false},
		{"quoted link", `Give an explanation of "with one CSV row per item" and columns position, actor, instruction.`, prose, false},
		{"metadata mention", "The metadata says give the outline with one CSV row per item and columns position, actor, instruction.", prose, false},
		{"example requested", "Give the example text with one CSV row per item and columns position, actor, instruction.", prose, false},
		{"ambiguous names defer width", "Give the outline with one CSV row per item and columns position, actor and instruction.", prose, false},
		{"duplicate names defer width", "Give the outline with one CSV row per item and columns position, actor, actor.", prose, false},
		{"quoted names defer width", `Give the outline with one CSV row per item and columns "position", "actor", "instruction".`, prose, false},
		{"different clause names do not set width", "Give the outline with one CSV row per item. The metadata columns are position, actor, instruction.", prose, false},
		{"dotted names defer width", "Give the outline with one CSV row per item and columns record.position, actor, instruction.", prose, false},
		{"unicode prefix positions", "Give the Ⱥİ overview with one CSV row per item and columns position, actor, instruction.", prose, true},
		{"unicode quoted prefix", `Give the "Ⱥİ" overview with one CSV row per item and columns position, actor, instruction.`, prose, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckReuseAnswerContract(tc.question, tc.answer)
			if (len(got) > 0) != tc.reject {
				t.Fatalf("format rejection=%v want=%v reasons=%v", len(got) > 0, tc.reject, got)
			}
		})
	}
	for _, names := range []string{strings.Repeat("x", 65), strings.Repeat("a,", 32) + "z"} {
		if got := CheckReuseAnswerContract("Give notes with one CSV row per item and columns "+names+".", prose); len(got) != 0 {
			t.Fatal("unbounded name list supplied a false width", got)
		}
	}
}

func TestDelimitedRowsMetaNounsDeferInSingularAndPlural(t *testing.T) {
	for _, format := range []string{"CSV", "TSV"} {
		for _, noun := range []string{"example", "examples", "phrase", "phrases", "quotation", "quotations", "label", "labels", "heading", "headings", "template", "templates"} {
			question := "Give " + noun + " of instructions with one " + format + " row per item and columns item_id, actor, action."
			if got := CheckReuseAnswerContract(question, "This describes an instruction rather than providing tabular rows."); len(got) != 0 {
				t.Fatalf("meta noun became output direction: %s -> %v", noun, got)
			}
		}
	}
	for _, question := range []string{
		"Give examples of instructions with one CSV row per item and columns item_id, actor, action.",
		"Give an example of an instruction with one CSV row per item and columns item_id, actor, action.",
	} {
		if got := CheckReuseAnswerContract(question, "The example explains a tabular instruction."); len(got) != 0 {
			t.Fatal("consumed meta regression forced CSV", got)
		}
	}
}
