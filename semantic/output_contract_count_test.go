package semantic

import "testing"

func TestOutputContractCountBeforeFormat(t *testing.T) {
	for _, question := range []string{
		"Return readiness as exactly three CSV fields: blocker, owner, window_start.",
		"Provide readiness in 3 CSV columns.",
	} {
		if got := CheckReuseAnswerContract(question, "The release is blocked and Mira owns the remaining check."); len(got) == 0 {
			t.Fatalf("explicit CSV width did not reject a prose record: %q", question)
		}
		if got := CheckReuseAnswerContract(question, "blocked,Mira,midnight"); len(got) != 0 {
			t.Fatalf("valid CSV was rejected: %v", got)
		}
	}
}
