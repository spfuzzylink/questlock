package semantic

import (
	"strings"
	"testing"
)

func TestOutputContractRecognizedPresentationAndParserEdges(t *testing.T) {
	for _, test := range []struct {
		name, question, answer string
		reject                 bool
	}{
		{"json object", "Return the result as a JSON object.", `{"value":7}`, false},
		{"json array", "Return only a JSON array.", `[7]`, false},
		{"json scalar", "Return only JSON.", `7`, false},
		{"wrong json type", "Return only a JSON object.", `[7]`, true},
		{"wrong array type", "Return the result in JSON array.", `{"value":7}`, true},
		{"json fence", "Return only JSON.", "```json\n{\"value\":7}\n```", true},
		{"invalid json", "Return the result as JSON.", `{"value":7`, true},
		{"source shape is not output shape", "The source JSON array contains records. Return the result as a JSON object.", `{"value":7}`, false},
		{"csv three columns", "Return the result as CSV with exactly three columns.", "a,b,c\n1,2,7", false},
		{"hyphenated csv", "Return the result as a three-column CSV.", "a,b,c\n1,2,7", false},
		{"hyphenated csv rejects prose", "Return the result as a 3-column CSV.", "The value is 7.", true},
		{"quoted csv commas and newlines", "Return the result as CSV with three fields.", "\"a,b\",c,d\r\n\"x\ny\",\"z\"\"q\",7\r\n", false},
		{"csv bad quotes", "Return the result as CSV with three columns.", "\"a,b,c\n1,2,7", true},
		{"csv bad width", "Return the result as CSV with three columns.", "a,b,c\n1,7", true},
		{"zero columns", "Return the result as CSV with zero columns.", "value,7", true},
		{"explicit zero columns", "Return the result as CSV with 0 columns.", "value,7", true},
		{"large explicit width", "Return the result as CSV with 100 columns.", "value,7", true},
		{"overflow explicit width", "Return the result as CSV with 9999999999999999999999 columns.", "value,7", true},
		{"one csv field", "Return the result as CSV with one column.", "value 7", false},
		{"tsv empty edge fields", "Return the result as TSV with three fields.", "\t7\t\n", false},
		{"tsv wrong width", "Return the result as TSV with three fields.", "value\t7", true},
		{"markdown", "Return the result as a Markdown table with two columns.", "| key | value |\n| --- | ---: |\n| x | 7 |", false},
		{"markdown escaped pipe", "Return the result as a two-column Markdown table.", "| key | value |\n| --- | --- |\n| x\\|y | 7 |", false},
		{"markdown extra empty columns", "Return the result as a Markdown table with two columns.", "||key|value||\n||---|---||\n||x|7||", true},
		{"markdown bad separator", "Return the result as a Markdown table.", "| key | value |\n| hello | --- |\n| x | 7 |", true},
		{"source width is not output width", "The source has three columns. Return the result as a Markdown table with two columns.", "| key | value |\n| --- | --- |\n| x | 7 |", false},
		{"generic number exponent", "Return only the number.", "-1.5e2", false},
		{"numeric suffix", "Return a numeric-only answer.", "7", false},
		{"numeric prose", "Return only the numeric result.", "The value is 7.", true},
		{"signed integer", "Return only an integer.", "-7", false},
		{"integer rejects decimal", "Return only an integer.", "7.0", true},
		{"integer rejects exponent", "Return only an integer.", "7e0", true},
		{"decimal", "Return only a decimal.", "-.7", false},
		{"decimal rejects exponent", "Return only a decimal.", "7e0", true},
		{"no punctuation", "Return only the number with no punctuation.", "007", false},
		{"no punctuation rejects sign", "Return only the number with no punctuation.", "-7", true},
		{"source punctuation is not output restriction", "The source has no punctuation. Return only the number.", "-7", false},
		{"csv subject", "What should I know about exporting CSV files?", "The export contains the approved records.", false},
		{"source quoted command", "Explain the phrase \"return as three-column CSV\" in prose.", "It describes a table export.", false},
		{"quoted source format link", "Return an explanation of \"CSV as JSON\" in prose.", "It describes a format conversion.", false},
		{"quoted format name", "Return the result as a three-column \"CSV\".", "a,b,7", false},
		{"quoted format name mismatch", "Return the result as a three-column \"CSV\".", "The value is 7.", true},
		{"conflicting requested formats", "Return only JSON. Return the result as CSV with one column.", `7`, true},
		{"conflicting formats in one sentence", "Return as CSV and return as JSON.", `7`, true},
		{"empty requested number", "Return only the number.", "\n ", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if reasons := CheckReuseAnswerContract(test.question, test.answer); (len(reasons) > 0) != test.reject {
				t.Fatalf("unexpected format decision: reasons=%v reject=%v", reasons, test.reject)
			}
		})
	}
}

func TestOutputContractRejectsInvalidTextAndRemainsBounded(t *testing.T) {
	for _, pair := range [][2]string{
		{"", "7"}, {string([]byte{0xff}), "7"}, {"Return only JSON.", string([]byte{0xff})},
		{strings.Repeat("x", MaxInputBytes+1), "7"}, {"Return only JSON.", strings.Repeat("x", 131073)},
		{"Return the answer as CSV with two columns.", strings.Repeat("x,7\n", 1025)},
		{"Return the answer as a Markdown table.", "|a|b|\n|---|---|\n" + strings.Repeat("|x|7|\n", 1023)},
	} {
		if len(CheckReuseAnswerContract(pair[0], pair[1])) == 0 {
			t.Fatal("invalid or oversized presentation passed")
		}
	}
	if len(CheckReuseAnswerContract("Return the answer as CSV with two columns.", strings.Repeat("x,7\n", 1024))) != 0 {
		t.Fatal("bounded CSV row limit rejected its supported boundary")
	}
}
