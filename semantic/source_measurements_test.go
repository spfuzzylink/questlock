package semantic

import (
	"strings"
	"testing"
)

func TestSourceMeasurementQualifiersAndAliases(t *testing.T) {
	for _, test := range []struct {
		name, evidence, answer string
		reject                 bool
	}{
		{"average is not p95", "The p95 latency must be below 240 ms.", "Average latency must be below 240 ms.", true},
		{"p95 is not average", "Average latency must stay below 240 ms.", "p95 latency must stay below 240 ms.", true},
		{"different percentile", "p95 latency was 240 ms.", "p99 latency was 240 ms.", true},
		{"new peak", "Latency was 240 ms.", "Peak latency was 240 ms.", true},
		{"new minimum before number", "Latency was 240 ms.", "Minimum latency was 240 ms.", true},
		{"new maximum after number", "Latency was 240 ms.", "240 ms was the maximum latency.", true},
		{"mean average alias", "Mean latency was 240 ms.", "Average latency was 240 ms.", false},
		{"arithmetic mean alias", "Average latency was 240 ms.", "Arithmetic mean latency was 240 ms.", false},
		{"arithmetic mean hyphen alias", "Average latency was 240 ms.", "Arithmetic-mean latency was 240 ms.", false},
		{"median p50 alias", "Median latency was 240 ms.", "p50 latency was 240 ms.", false},
		{"p50 median alias", "p50 latency was 240 ms.", "Median latency was 240 ms.", false},
		{"ordinal percentile alias", "The 95th percentile latency was 240 ms.", "p95 latency was 240 ms.", false},
		{"percentile suffix alias", "Percentile 95 latency was 240 ms.", "The 95th-percentile latency was 240 ms.", false},
		{"upper case alias", "P95 latency was 240 ms.", "The 95TH PERCENTILE latency was 240 ms.", false},
		{"p100", "The 100th percentile latency was 240 ms.", "p100 latency was 240 ms.", false},
		{"percentile digits alone are not a measurement", "Latency observations were collected.", "The report discusses p95 and p99.", true},
		{"no measured quantity", "Latency observations were collected.", "The report discusses p95.", false},
		{"unsupported nonstatistical text stays model work", "The queue is paused.", "The queue is active.", false},
		{"matching qualifier does not prove value", "Mean latency was 240 ms.", "Mean latency was 900 ms.", false},
		{"matching qualifier does not prove association", "Mean read latency was 240 ms; write latency was not measured.", "Mean write latency was 240 ms.", false},
		{"decimal percentile must not collapse to integer prefix", "p99.9 latency was 240 ms.", "p99.99 latency was 240 ms.", true},
		{"decimal percentile alias", "The 99.9th percentile latency was 240 ms.", "p99.9 latency was 240 ms.", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := CheckSourceMeasurements(test.evidence, test.answer)
			if (len(got) > 0) != test.reject {
				t.Fatalf("rejection=%v want=%v", got, test.reject)
			}
			if len(got) > 0 && (len(got) != 1 || got[0] != "source_statistic_not_in_evidence") {
				t.Fatalf("diagnostic leaked data or changed contract: %v", got)
			}
		})
	}
}

func TestSourceMeasurementScreenIsBoundedAndHandlesInvalidText(t *testing.T) {
	for _, input := range [][2]string{
		{string([]byte{0xff}), "Mean is 7."},
		{"Mean is 7.", string([]byte{0xff})},
		{strings.Repeat("x", 262145), "Mean is 7."},
		{"Mean is 7.", strings.Repeat("x", 131073)},
	} {
		got := CheckSourceMeasurements(input[0], input[1])
		if len(got) != 1 || got[0] != "invalid_source_measurement_text" {
			t.Fatalf("unsafe input was not rejected with a fixed code: %v", got)
		}
	}
	if len(CheckSourceMeasurements(strings.Repeat("x", 262144), strings.Repeat("x", 131072))) != 0 {
		t.Fatal("valid boundary-length input was rejected")
	}
}
