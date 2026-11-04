package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

// ResourceDeferralState represents the state of resource deferral during evaluation.
type ResourceDeferralState struct {
	AdmissionPolicy        string  `json:"admission_policy"` // Policy used for admission decision
	ResourceQueueID        string  `json:"resource_queue_id"` // Queue identifier for resource tracking
	DelayedTimestamp       time.Time `json:"delayed_timestamp"`  // When the resource was deferred
	Reason                 string   `json:"reason"`            // Why deferral occurred
	SimilarityScore        float64  `json:"similarity_score"` // Similarity score triggering deferral
}

// semanticEvalResourceDeferred checks if a pair status indicates resource deferral.
func semanticEvalResourceDeferred(row semanticEvalPair) bool {
	return row.Status == "resource_deferred" && row.ErrorCategory == "resource_deferred" && !row.Served
}

// deferResourceAdmission creates deferred state for a semantic evaluation pair.
func deferResourceAdmission(pairID string, threshold float64, similarity *float64) (*ResourceDeferralState, error) {
	var reason string
	switch {
	case threshold < 0.5:
		reason = "insufficient_similarity"
	case threshold < 0.8:
		reason = "below_admission_threshold"
	default:
		reason = "admission_resource_unavailable"
	}
	der := &ResourceDeferralState{AdmissionPolicy: "factorized-v1", Reason: reason, SimilarityScore: *similarity}
	if similarity == nil {
		der.SimilarityScore = 0.7 // Default for unknown thresholds
	} else if *similarity >= 0.85 && *similarity < 0.95 {
		reason = "limited_capacity_available"
	}
	t := time.Now()
	der.DelayedTimestamp = t
	h := sha256.Sum256([]byte(pairID + der.Reason))
	der.ResourceQueueID = hex.EncodeToString(h[:])
	return der, nil
}

// ResourceAdmissionConfig represents the admission configuration for resource deferral.
type ResourceAdmissionConfig struct {
	ConcurrentLimit      int                    `json:"concurrent_limit"`
	QueueSize            int                    `json:"queue_size"`
	BaseDelayMS          int                    `json:"base_delay_ms"`
	ResourceThreshold     float64                `json:"resource_threshold"` // 0.9 = high priority, deferral triggers
	SimilarityDeferral    float64                `json:"similarity_deferral"` // Similarity score below which always defer
	AdmissionStrategy     string                 `json:"admission_strategy"` // "first-come" or "weighted-priority"
}
