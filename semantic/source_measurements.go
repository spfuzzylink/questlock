package semantic

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// SourceMeasurementGuardVersion participates in the factorized policy identity.
const SourceMeasurementGuardVersion = 1

var sourceStatistic = regexp.MustCompile(`(?i)\b(?:p[0-9]+(?:\.[0-9]+)?|[0-9]+(?:\.[0-9]+)?(?:st|nd|rd|th)[ -]+percentile|percentile[ -]+[0-9]+(?:\.[0-9]+)?|arithmetic[ -]+mean|mean|average|median|minimum|maximum|peak)\b`)
var sourceQuantity = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
var sourceDigits = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

// CheckSourceMeasurements is a rejection-only screen for explicit statistical
// qualifiers near numbers. A saved answer cannot introduce a different statistic
// merely because its threshold matches. Mean/average and median/p50 are aliases.
// Passing does NOT establish a claim's value, association, arithmetic or truth;
// the mandatory contextual source and applicability checks still do that work.
// It can conservatively reject valid derived statistics or ambiguous wording.
func CheckSourceMeasurements(evidence, answer string) []string {
	if !utf8.ValidString(evidence) || !utf8.ValidString(answer) || len(evidence) > 262144 || len(answer) > 131072 {
		return []string{"invalid_source_measurement_text"}
	}
	supported := make(map[string]bool)
	for _, match := range sourceStatistic.FindAllString(evidence, -1) {
		supported[sourceStatisticName(match)] = true
	}
	for _, loc := range sourceStatistic.FindAllStringIndex(answer, -1) {
		// Do not count the percentile's own digits as a measured quantity.
		// Byte slices are bounded search windows, not exported text.
		left, right := max(0, loc[0]-96), min(len(answer), loc[1]+96)
		if !sourceQuantity.MatchString(answer[left:loc[0]]) && !sourceQuantity.MatchString(answer[loc[1]:right]) {
			continue
		}
		if !supported[sourceStatisticName(answer[loc[0]:loc[1]])] {
			return []string{"source_statistic_not_in_evidence"}
		}
	}
	return nil
}

func sourceStatisticName(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "mean", "arithmetic mean", "arithmetic-mean", "average":
		return "mean"
	case "median":
		return "p50"
	}
	if strings.HasPrefix(s, "p") || strings.Contains(s, "percentile") {
		if n := sourceDigits.FindString(s); n != "" {
			parts := strings.SplitN(n, ".", 2)
			whole := strings.TrimLeft(parts[0], "0")
			if whole == "" {
				whole = "0"
			}
			if len(parts) == 2 {
				if fraction := strings.TrimRight(parts[1], "0"); fraction != "" {
					whole += "." + fraction
				}
			}
			return "p" + whole
		}
	}
	return s
}
