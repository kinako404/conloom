package obs

import (
	"testing"
	"time"
)

func TestPresence_SeenAndSnapshot(t *testing.T) {
	p := NewPresence()
	t1 := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	p.Seen("qq-main", t1)

	snap := p.Snapshot()
	if !snap["qq-main"].Equal(t1) {
		t.Fatalf("Snapshot()[qq-main] = %v, want %v", snap["qq-main"], t1)
	}

	snap["qq-main"] = t1.Add(time.Hour)
	snap["other"] = t1
	again := p.Snapshot()
	if !again["qq-main"].Equal(t1) || len(again) != 1 {
		t.Fatalf("Snapshot() after editing a copy = %v, want only qq-main at %v", again, t1)
	}

	t2 := t1.Add(time.Minute)
	p.Seen("qq-main", t2)
	if got := p.Snapshot()["qq-main"]; !got.Equal(t2) {
		t.Fatalf("Snapshot()[qq-main] = %v, want %v after a later Seen", got, t2)
	}

	var nilP *Presence
	nilP.Seen("qq-main", t1)
	if got := nilP.Snapshot(); len(got) != 0 {
		t.Fatalf("nil Snapshot() = %v, want empty", got)
	}
}
