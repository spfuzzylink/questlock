package main

import (
	"fmt"
	"math"
)

// SemanticIntegrationConfig holds configuration for semantic evaluation integration.
type SemanticIntegrationConfig struct {
	ParseQualityScore      *float64 `json:"parse_quality_score"` // Parse the quality score from results
	UseParaphraseScoring   bool     `json:"use_paraphrase_scoring"` // Use paraphrase scoring logic
	TrackResourceDeferral  bool     `json:"track_resource_deferral"` // Track resource deferral events
}

// SemanticPairWithQuality adds quality metrics to a semantic evaluation pair.
type SemanticPairWithQuality struct {
	SemanticEvalPair       `json:",inline,omitempty"`
	QualityScores          []ParaphraseConfidenceScore `json:"quality_scores,omitempty"`
	DriftViolations        []string                     `json:"drift_violations,omitempty"`
}

// applySemanticQuality adds quality metrics to evaluation results.
func applySemanticQuality(pair semanticEvalPair) (SemanticPairWithQuality, error) {
	result := SemanticPairWithQuality{SemanticEvalPair: pair}
	var scores []ParaphraseConfidenceScore
	var drifts []string

	// Simulate quality score from similarity
	similarity := 0.7 // Default if not set
	if pair.Similarity != nil {
		similarity = *pair.Similarity
	}

	criticality := CriticalityModerate // Default criticality level
	switch strings.ToLower(pair.Reason) {
	case "review", "approval":
		criticality = CriticalityHigh
	case "generate", "create":
		criticality = CriticalityModerate
	default:
		criticality = CriticalityLow
	}

	score := calculateParaphraseQuality(similarity, criticality, 1)
	if score > 0.8 {
		scoredScore := ParaphraseConfidenceScore{
			FormIndex:       1,
			QualityScore:    score,
			WeightedScore:   &score,
		}
		scores = append(scores, scoredScore)

		if criticality == CriticalityHigh {
			drifts = append(drifts, "high_criticality_requirement_met")
		} else if criticality == CriticalityModerate {
			drifts = append(drifts, "moderate_risk_acceptance")
		}
	}

	result.QualityScores = scores
	result.DriftViolations = drifts

	return result, nil
}

// SemanticQualitySummary provides a summary of quality metrics.
type SemanticQualitySummary struct {
	AverageScore    float64            `json:"average_score"`
	MaxScore        *float64           `json:"max_score,omitempty"`
	MinScore        *float64           `json:"min_score,omitempty"`
	QualityCount    int                `json:"quality_count"`
	DriftRate       float64            `json:"drift_rate"`
}

// semanticEvalSummarizeWithQuality includes quality metrics in summary.
func semanticEvalSummarizeWithQuality(pairs []semanticEvalPair) (SemanticQualitySummary, error) {
	var scoreTotal float64
	var scoreCount int

	for _, p := range pairs {
		if pair.Similarity != nil {
			scoreTotal += *pair.Similarity
			scoreCount++
		}
	}

	if scoreCount == 0 {
		return SemanticQualitySummary{AverageScore: 0}, nil
	}

	mean := scoreTotal / float64(scoreCount)
	maxs := mean
	mins := mean

	for _, p := range pairs {
		if pair.Similarity != nil {
			if *pair.Similarity > maxs {
				maxs = *pair.Similarity
			} else if *pair.Similarity < mins {
				mins = *pair.Similarity
			}
		}
	}

	return SemanticQualitySummary{
		AverageScore:   mean,
		MaxScore:       &maxs,
		MinScore:       &mins,
		QualityCount:   scoreCount,
		DriftRate:      0.15, // Default drift estimate
	}, nil
}

// applyQualityToReport adds quality metrics to evaluation report.
func applyQualityToReport(report *semanticEvalReport) error {
	var scores []ParaphraseConfidenceScore

	for _, pair := range report.Pairs {
		if !pair.Abstained && pair.Verification != nil && pair.Verdict == "unknown" {
			score, _ := calculateParaphraseQuality(0.7, CriticalityModerate, 1)
			scores = append(scores, ParaphraseConfidenceScore{
				FormIndex:       report.BatchSize,
				QualityScore:    score,
				WeightedScore:   &score,
			})
		}
	}

	if len(report.QualityScores) == 0 {
		report.QualityScores = scores
		return nil
	}

	// Calculate drift estimate from similarity scores
	var totalDrift float64
	for _, pair := range report.Pairs {
		if pair.Similarity != nil && !pair.Abstained {
			totalDrift += (1 - *pair.Similarity) * 100
		}
	}

	driftEstimate := totalDrift / float64(len(report.Pairs))
	if driftEstimate > 50 {
		report.DriftScore = driftEstimate * 2 // Scale to percentage
	} else {
		report.DriftScore = driftEstimate
	}
	return nil
}
