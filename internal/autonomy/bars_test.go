package autonomy

import (
	"math"
	"testing"
	"time"
)

func TestBand(t *testing.T) {
	tests := []struct {
		v    float64
		want string
	}{
		{-5, "很低"},
		{0, "很低"},
		{19.4, "很低"},
		{19.6, "偏低"},
		{39.4, "偏低"},
		{40, "中等"},
		{59, "中等"},
		{59.4, "中等"},
		{60, "偏高"},
		{79.6, "很高"},
		{100, "很高"},
		{150, "很高"},
	}
	for _, tt := range tests {
		if got := Band(tt.v); got != tt.want {
			t.Errorf("Band(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestDecay(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	tests := []struct {
		name     string
		v0       float64
		baseline int
		halfLife time.Duration
		elapsed  time.Duration
		want     float64
	}{
		{"one half-life halfway down", 90, 60, 6 * time.Hour, 6 * time.Hour, 75},
		{"two half-lives", 90, 60, 6 * time.Hour, 12 * time.Hour, 67.5},
		{"below baseline rises", 20, 60, 4 * time.Hour, 4 * time.Hour, 40},
		{"at baseline stays", 60, 60, 6 * time.Hour, 3 * time.Hour, 60},
		{"elapsed zero keeps v0", 90, 60, 6 * time.Hour, 0, 90},
		{"elapsed negative keeps v0", 90, 60, 6 * time.Hour, -time.Hour, 90},
		{"no half-life is baseline", 90, 60, 0, time.Hour, 60},
	}
	for _, tt := range tests {
		if got := Decay(tt.v0, tt.baseline, tt.halfLife, tt.elapsed); !near(got, tt.want) {
			t.Errorf("%s: Decay(%v, %d, %v, %v) = %v, want %v", tt.name, tt.v0, tt.baseline, tt.halfLife, tt.elapsed, got, tt.want)
		}
	}
}

func TestClamp(t *testing.T) {
	for v, want := range map[float64]float64{-1: 0, 0: 0, 50.5: 50.5, 100: 100, 101: 100} {
		if got := Clamp(v); got != want {
			t.Errorf("Clamp(%v) = %v, want %v", v, got, want)
		}
	}
}
