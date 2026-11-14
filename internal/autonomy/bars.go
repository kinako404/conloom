package autonomy

import (
	"math"
	"time"
)

// Band names the band of a bar value, rounded to an integer, shown to the
// model instead of the number.
func Band(v float64) string {
	switch r := math.Round(v); {
	case r < 20:
		return "很低"
	case r < 40:
		return "偏低"
	case r < 60:
		return "中等"
	case r < 80:
		return "偏高"
	default:
		return "很高"
	}
}

// Decay returns a bar value v0 after elapsed: its distance to baseline halves
// every halfLife. A non-positive elapsed keeps v0; a non-positive halfLife
// returns baseline.
func Decay(v0 float64, baseline int, halfLife, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return v0
	}
	if halfLife <= 0 {
		return float64(baseline)
	}
	b := float64(baseline)
	return b + (v0-b)*math.Pow(0.5, float64(elapsed)/float64(halfLife))
}

// Clamp limits a bar value to [0, 100].
func Clamp(v float64) float64 {
	return min(max(v, 0), 100)
}
