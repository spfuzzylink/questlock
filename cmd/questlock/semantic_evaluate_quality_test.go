package main

import (
	"math"
	"testing"
)

func TestCalculateParaphraseQuality(t *testing.T) {
	tests := []struct {
		name       string
		similarity float64
		criticality int
		expect     float64
	}{
		{"low_similarity_high_criticality", 0.5, 2, 0.475},
		{"high_similarity_low_criticality", 0.95, 1, 0.95},
		{"exact_match_high_criticality", 1.0, 2, 0.8},
		{"invalid_nan", math.NaN(), 1, 0.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := calculateParaphraseQuality(tc.similarity, tc.criticality)
			// Allow small floating point tolerance
			if result != tc.expect {
				diff := math.Abs(result - tc.expect)
				if diff < 0.01 {
					t.Logf("Result %.2f within expected %.2f", result, tc.expect)
				} else {
					t.Errorf("Expected %f, got %f", tc.expect, result)
				}
			}
		})
	}
}

func TestQualityThresholdCheck(t *testing.T) {
	tests := []struct {
		name     string
		score    float64
		expected bool // should fail (return non-nil error)
	}{
		{"low_score", 0.7, true},   // below 0.85 threshold - should fail
		{"moderate_score", 0.9, false}, // above threshold - should pass
		{"high_score", 1.0, false},     // exact match - should pass
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failed := calculateQualityThreshold(tc.score, 0.85)
			if failed != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, failed)
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
		t.Run("variation_"+string(rune(i+49)), func(t *testing.T) {
			drift := calculateDrift(original, variation)
			if drift > 0.3 {
				t.Logf("Variation %d detected drift: %.2f", i, drift)
			}
		})
	}
}
