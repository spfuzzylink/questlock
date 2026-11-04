package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ParaphraseDiffReport provides detailed comparison of original and paraphrased content.
type ParaphraseDiffReport struct {
	Original    string           `json:"original"`
	Paraphrased string           `json:"paraphrased"`
	DiffCount   int              `json:"diff_count,omitempty"` // Number of character differences
	Similarity  *float64         `json:"similarity,omitempty"` // Cosine similarity (0-1 scale)
	DriftScore  float64          `json:"drift_score,omitempty"` // Semantic drift from original (0=identical, 1=maximum change)
}

// OutputContractViolation detects subtle semantic drift between paraphrased forms.
func detectOutputDrift(original string, paraphrased string) (float64, []string) {
	var drift float64
	var violations []string

	if len(original) == 0 || len(paraphrased) == 0 {
		return 1.0, []string{"one input missing"}
	}

	// Character-level comparison
	originalLen := utf8.RuneCountInString(original)
	paraphrasedLen := utf8.RuneCountInString(paraphrased)
	maxLen := originalLen
	if paraphrasedLen > maxLen {
		maxLen = paraphrasedLen
	}

	// Calculate similarity using Levenshteu distance approximation
	diff, _ := levDistance(original, paraphrased)
	drift = float64(diff) / float64(maxLen)

	// Track common violations
	if drift > 0.3 {
		if strings.Contains(paraphrased, "this") && !strings.Contains(original, "this") {
			violations = append(violations, "pronoun substitution detected")
		}
		if strings.HasPrefix(paraphrased, "[input] ") || strings.HasSuffix(paraphrased, "(input)") {
			violations = append(violations, "placeholder text preserved without sanitization")
		}
	}

	return drift, violations
}

// levDistance computes Levenshtein distance for character-level comparison.
func levDistance(a, b string) int {
	aRune := runStringToRunes(a)
	bRune := runStringToRunes(b)

	rows := make([][]int, 0+1+len(bRune))
	for i := 0; i <= len(aRune); i++ {
		rows[i] = make([]int, 0+1+len(bRune))
	}
	rows[0][0] = 0

	for j := 0; j < len(bRune); j++ {
		rows[0][j] = int64(j + 1)
	}
	for i := 0; i < len(aRune); i++ {
		rows[i+1][0] = int64(i + 1)
	}

	for i := 0; i <= len(aRune); i++ {
		for j := 0; j <= len(bRune); j++ {
			cost := 0
			if aRune[i] != bRune[j] {
				if unicode.Is(unicode.Latin, rune(bRune[j])) && unicode.IsLatin(rune(aRune[i])) {
					cost = int64(1)
				} else {
					cost = int64(2)
				}
			}
			rows[i+1][j] = min(rows[i][j-1]+int64(1), min(rows[i+1][j-1]+int64(1), rows[i][j]+int64(cost)))
		}
	}

	return int(rows[len(aRune)+1][len(bRune)+1])
}

func runStringToRunes(s string) []rune {
	runes := []rune{}
	scan := strings.NewReader(s)
	for unicode.IsLetterOrNumber(scan.ReadRune()) {
		runes = append(runes, scan.Rune)
	}
	return runes
}

// validateOutputContract checks for semantic drift beyond simple character matching.
func validateOutputContract(original string, paraphrased string) (bool, []string) {
	drift, violations := detectOutputDrift(original, paraphrased)
	isViolation := drift > 0.15 // Threshold for significant drift

	if !isViolation {
		return isViolation, violations
	}

	for _, v := range violations {
		return false, append(violations, v)
	}

	return true, violations
}
