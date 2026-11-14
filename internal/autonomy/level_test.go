package autonomy

import (
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestDailyCapAndGap(t *testing.T) {
	tests := []struct {
		level string
		cap   int
		gap   time.Duration
	}{
		{conloomapi.PrivateLevelOff, 0, 0},
		{conloomapi.PrivateLevelLow, 1, 8 * time.Hour},
		{conloomapi.PrivateLevelMid, 2, 4 * time.Hour},
		{conloomapi.PrivateLevelHigh, 4, 2 * time.Hour},
		{"", 0, 0},
		{"medium", 0, 0},
	}
	for _, tt := range tests {
		if got := DailyCap(tt.level); got != tt.cap {
			t.Errorf("DailyCap(%q) = %d, want %d", tt.level, got, tt.cap)
		}
		if got := MinGap(tt.level); got != tt.gap {
			t.Errorf("MinGap(%q) = %v, want %v", tt.level, got, tt.gap)
		}
	}
	if MaxUnanswered != 2 {
		t.Errorf("MaxUnanswered = %d, want 2", MaxUnanswered)
	}
}
