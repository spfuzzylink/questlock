package admission

import (
	"context"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

type systemSource struct{}

func SystemSource() Source { return systemSource{} }
func (systemSource) Sample(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return sampleSystem(ctx)
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, ErrSignals
	}
	return b, nil
}

func nonnegative(text string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "+") {
		return 0, ErrSignals
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrSignals
	}
	return n, nil
}
func decimalMilli(text string, maxValue float64) (int64, error) {
	x, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < 0 || x > maxValue {
		return 0, ErrSignals
	}
	return int64(math.Ceil(x * 1000)), nil
}

// PSI avg10 is a percentage of time stalled, not CPU utilization. Parse only
// the documented named rows/fields, rejecting malformed or duplicate signals.
func parsePSI(b []byte, requireFull bool) (int64, error) {
	rows := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || (f[0] != "some" && f[0] != "full") {
			return 0, ErrSignals
		}
		if _, ok := rows[f[0]]; ok {
			return 0, ErrSignals
		}
		fields := map[string]string{}
		for _, v := range f[1:] {
			k, x, ok := strings.Cut(v, "=")
			if !ok {
				return 0, ErrSignals
			}
			if _, ok := fields[k]; ok {
				return 0, ErrSignals
			}
			fields[k] = x
		}
		if len(fields) != 4 {
			return 0, ErrSignals
		}
		var value int64
		for _, key := range []string{"avg10", "avg60", "avg300"} {
			n, err := decimalMilli(fields[key], 100)
			if err != nil {
				return 0, err
			}
			if key == "avg10" {
				value = n
			}
		}
		if _, err := nonnegative(fields["total"]); err != nil {
			return 0, err
		}
		rows[f[0]] = value
	}
	if _, ok := rows["some"]; !ok {
		return 0, ErrSignals
	}
	if requireFull {
		if _, ok := rows["full"]; !ok {
			return 0, ErrSignals
		}
	}
	return max(rows["some"], rows["full"]), nil
}
