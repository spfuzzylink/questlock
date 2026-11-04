package main

import (
	"math"
	"strconv"
)

// ParaphraseConfidenceScore represents a confidence score for a specific form.
// Form indices are 1-based, counting from the first approved form in the sequence.
type ParaphraseConfidenceScore struct {
	FormIndex       int           `json:"form_index"`    // The index (1-based) of this form in the canonical sequence
	QualityScore    float64        `json:"quality_score"` // Raw confidence score from 0 to 1
	WeightedScore   *float64       `json:"weighted_score,omitempty"` // Score adjusted by criticality weight
}

// CriticalityLevel indicates the operational importance of an input intent.
// Higher levels impose stricter reuse thresholds to prevent safety issues.
type CriticalityLevel int

const (
	CriticalityLow     CriticalityLevel = iota
	CriticalityModerate
	CriticalityHigh
)

// criticalityToWeight converts a level to its impact multiplier for confidence scoring.
// Lower criticality gets higher weight boost; high criticality requires stricter thresholds.
func criticalityToWeight(level CriticalityLevel) float64 {
	switch level {
	case CriticalityLow:
		return 1.0 // Low impact, no adjustment needed
	case CriticalityModerate:
		return 0.95 // Slightly stricter threshold for moderate importance
	default: // CriticalityHigh
		return 0.80 // Substantial reduction to prevent unsafe acceptances
	}
}

// calculateParaphraseQuality computes form-specific confidence with weight adjustments.
// Quality starts at raw cosine similarity and gets modulated by criticality level and position.
func calculateParaphraseQuality(rawSimilarity float64, intentCriticality CriticalityLevel, formIndex int) (float64, float64) {
	if math.IsNaN(rawSimilarity) || math.IsInf(rawSimilarity, 0) || rawSimilarity < 0 || rawSimilarity > 1.0 {
		return 0.0, 0.0
	}
	if rawSimilarity >= 0.95 {
		// Exact or near-exact match: no adjustment needed beyond form position
		return rawSimilarity, rawSimilarity
	}
	criticalityWeight := criticalityToWeight(intentCriticality)
	formFactor := float64(formIndex-1) / float64(32) // Cap at 32 forms per policy
	decayFactor := 0.05 * (criticalityWeight + formFactor)
	basicScore := rawSimilarity * (1 - decayFactor)
	if basicScore < 0 {
		basicScore = 0
	}
	return basicScore, criticalityWeight
}

// applyParaphraseQualityScoring applies quality metrics to a batch of paraphrase forms.
func applyParaphraseQualityScoring(forms []string, intentCriticality CriticalityLevel) ([]ParaphraseConfidenceScore, error) {
	var scores []ParaphraseConfidenceScore
	for index := 1; index <= len(forms) && len(scores) < 32; index++ {
		rawSimilarity := float64(index-1) / float64(32) // Default similarity from form position
		score := ParaphraseConfidenceScore{}
		score.FormIndex = index
		rawScore, weightedScore := calculateParaphraseQuality(rawSimilarity, intentCriticality, index)
		score.QualityScore = rawScore
		score.WeightedScore = &weightedScore
		scores = append(scores, score)
	}
	return scores, nil
}

// CriticalityIntent returns the operational importance of an input intent based on form content.
func IntentCriticality(form string) CriticalityLevel {
	formLower := strings.ToLower(form)
	switch formLower {
	case "review", "approve":
		return CriticalityHigh
	case "generate", "create":
		return CriticalityModerate
	default:
		return CriticalityLow
	}
}
