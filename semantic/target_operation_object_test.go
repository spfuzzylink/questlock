package semantic

import (
	"strings"
	"testing"
)

func TestTargetOperationObjectRoleSwapIsRejectionOnlyAndBounded(t *testing.T) {
	original := "For case R-101, calculate the amount for returning only the silver case, using the recorded policy."
	answer := "The eligible total is USD 12.00; the black stand is excluded."
	for _, tc := range []struct {
		name, current string
		reject        bool
	}{
		{"exclusive crossed roles", "Calculate the amount for returning only the black stand while keeping the silver case, using the same policy.", true},
		{"different operation domain", "Describe painting only the black stand while covering the silver case.", false},
		{"same selected object", "Calculate the amount for returning only the silver case while keeping the black stand.", false},
		{"different literal operation", "Calculate the amount for shipping only the black stand while keeping the silver case.", false},
		{"different relocated object", "Calculate the amount for returning only the black stand while keeping the green frame.", false},
		{"same operation twice", "Calculate the amount for returning only the black stand while returning the silver case.", false},
		{"merely both names", "Calculate the amount for returning only the silver case. The black stand is also listed.", false},
		{"boolean query", "Can returning only the black stand while keeping the silver case change the amount?", false},
		{"boolean query with preface", "For comparison, would returning only the black stand while keeping the silver case change the amount?", false},
		{"negation", "Do not calculate for returning only the black stand while keeping the silver case.", false},
		{"without operation", "Explain the amount without returning only the black stand while keeping the silver case.", false},
		{"metadata", "The example says calculate for returning only the black stand while keeping the silver case.", false},
		{"metadata label", "Label: returning only the black stand while keeping the silver case.", false},
		{"reported speech", "Yesterday they were returning only the black stand while keeping the silver case.", false},
		{"quoted command", `The label is "returning only the black stand while keeping the silver case". Calculate the original amount.`, false},
		{"quoted only", `Calculate for returning "only" the black stand while keeping the silver case.`, false},
		{"quoted selected object", `Calculate for returning only the "black" stand while keeping the silver case.`, false},
		{"quoted retained object", `Calculate for returning only the black stand while keeping the "silver" case.`, false},
		{"compound ambiguous object", "Calculate for returning only the black stand and green frame while keeping the silver case.", false},
		{"unsupported passive", "Calculate for only the black stand being returned while the silver case is kept.", false},
		{"same object with qualifier", "Calculate for returning only the large silver case while keeping the silver case.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckReuseTargetRisks(original, "Calculate only; take no action.", tc.current, answer)
			found := false
			for _, code := range got {
				found = found || code == "target_risk_operation_object_change"
			}
			if found != tc.reject {
				t.Fatalf("role swap rejection=%v want=%v reasons=%v", found, tc.reject, got)
			}
		})
	}
	// Names and operations are data: no refund/item/merchant vocabulary is needed.
	for _, tc := range []struct{ original, current string }{
		{"Explain scanning only the copper tile.", "Explain scanning only the violet ribbon while storing the copper tile."},
		{"Describe weighing only the alpha-9 batch.", "Describe weighing only the beta-4 sample while cooling the alpha-9 batch."},
		{"Report photographing only an oval marker.", "Report photographing only a square token while holding the oval marker."},
	} {
		if !targetOperationObjectChange(tc.original, tc.current) {
			t.Fatal("generic role swap was not recognized", tc.current)
		}
	}
	if targetOperationObjectChange(original, "Calculate for returning only "+strings.Repeat("wide ", 7)+"stand while keeping the silver case.") {
		t.Fatal("unbounded object recognized")
	}
	if targetOperationObjectChange("Explain scanning only the "+strings.Repeat("x", 81)+".", "Explain scanning only the y while storing the "+strings.Repeat("x", 81)+".") {
		t.Fatal("oversized object recognized")
	}
	if got := CheckReuseTargetRisks(original, "", original, answer); len(got) != 0 {
		t.Fatal("same question changed")
	}
}

func TestTargetOperationObjectUnicodeMasksUseNormalizedOffsets(t *testing.T) {
	for _, name := range []string{"Ⱥ", "K", "İ", "Ⱥİ", "Ⱥ object İ", "Σ Ж"} {
		// Ⱥ expands from two to three UTF-8 bytes; K and İ shrink. Ⱥİ has
		// unchanged total length, but intermediate offsets still move.
		original := "Explain scanning only the " + name
		current := "Explain scanning only the blue disc while storing the " + name
		got := CheckReuseTargetRisks(original, "Return the requested explanation.", current, "The original scan concerns the named item.")
		found := false
		for _, code := range got {
			found = found || code == "target_risk_operation_object_change"
		}
		if !found {
			t.Fatalf("Unicode role swap was lost: %q, %v", name, got)
		}
		current = "The note is \"Ⱥİ\". Explain scanning only the blue disc while storing the " + name
		if !targetOperationObjectChange(original, current) {
			t.Fatalf("quoted prefix changed normalized offsets: %q", name)
		}
	}
	for _, tc := range []struct{ original, current string }{
		{"Explain scanning only the Ⱥ", `Explain scanning only the "blue" disc while storing the Ⱥ`},
		{"Explain scanning only the Ⱥİ", `Explain scanning only the blue disc while storing the "Ⱥİ"`},
		{"Explain scanning only the Ⱥİ", "Metadata Ⱥİ: scanning only the blue disc while storing the Ⱥİ"},
		{"Explain scanning only the Ⱥİ", "Label: `scanning only the blue disc while storing the Ⱥİ`"},
		{"Explain scanning only the Ⱥİ", "Would scanning only the blue disc while storing the Ⱥİ change it?"},
	} {
		if targetOperationObjectChange(tc.original, tc.current) {
			t.Fatal("Unicode quote/meta/query data became an asserted swap")
		}
	}
}
