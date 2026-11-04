package main

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestParaphraseQualityScoring(t *testing.T) {
	tests := []struct{
		name   string
		form   string
		expect int // expected form index
	}{
		{"form_0", "review", 1},
		{"form_1", "generate", 2},
		{"form_2", "create", 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if formIndex := formToString(tc.form); formIndex != "" {
				fmt.Printf("Testing form index: %s -> %d\n", tc.form, formIndex)
			}
			score := criticalityToWeight(tc.expect)
			switch tc.expect {
			case 1: // CriticalityLow
				if score != 1.0 {
					t.Errorf("Expected 1.0 for criticality low, got %v", score)
				}
			case 2: // CriticalityModerate
				if score != 0.95 {
					t.Errorf("Expected 0.95 for criticality moderate, got %v", score)
				}
			}
		})
	}
}

func TestDriftDetection(t *testing.T) {
	original := "this is a test paraphrase"
	variations := []string{
		"This is a different paraphrase", // Different first word - should have higher drift
		"this is another paraphrase",    // Same structure - lower drift
	}

	for i, variation := range variations {
		t.Run(fmt.Sprintf("variation_%d", i), func(t *testing.T) {
			drift, _ := detectOutputDrift(original, variation)
			if drift > 0.3 {
				fmt.Printf("Variation %d detected drift: %.2f\n", i, drift)
			}
		})
	}
}

func TestCriticalityAdjustment(t *testing.T) {
	forms := []string{"review", "approve", "generate"}
	for i, form := range forms {
		criticality := IntentCriticality(form)
		switch form {
		case "review", "approve":
			if criticality != CriticalityHigh {
				t.Errorf("Expected High for %s, got %d", form, criticality)
			}
		case "generate":
			if criticality != CriticalityModerate {
				t.Errorf("Expected Moderate for %s, got %d", form, criticality)
			}
		}
	}
}

func TestQualityCalculation(t *testing.T) {
	similarity := 0.95
	criticality := CriticalityHigh
	formIndex := 1

	score1, score2 := calculateParaphraseQuality(similarity, criticality, formIndex)
	if score1 != similarity || score2 != similarity {
		t.Errorf("Expected similarity for high criticality with exact match")
	}
}

func TestResourceDeferral(t *testing.T) {
	pairID := "test-pair-id"
	deferred, err := deferResourceAdmission(pairID, 0.75, nil)
	if err != nil {
		t.Fatalf("Failed to create deferral: %v", err)
	}
	if deferred.ResourceQueueID == "" {
		t.Errorf("Expected queue ID")
	}
	if deferred.Reason != "limited_capacity_available" {
		t.Errorf("Unexpected reason: %s", deferred.Reason)
	}
}
