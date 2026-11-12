package memory

import (
	"strconv"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func affinityOf(m store.MemberMemory) string {
	if m.Affinity == nil {
		return "nil"
	}
	return strconv.Itoa(*m.Affinity)
}

func TestTierName(t *testing.T) {
	cases := map[int]string{
		0: "陌生", 19: "陌生",
		20: "认识", 39: "认识",
		40: "熟悉", 59: "熟悉",
		60: "亲近", 79: "亲近",
		80: "很要好", 100: "很要好",
	}
	for affinity, want := range cases {
		if got := TierName(affinity); got != want {
			t.Errorf("TierName(%d) = %q, want %q", affinity, got, want)
		}
	}
}

func TestClamp(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 57: 57, 100: 100, 130: 100} {
		if got := Clamp(in); got != want {
			t.Errorf("Clamp(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestApplyInteraction(t *testing.T) {
	m, changed := ApplyInteraction(store.MemberMemory{PlatformUserID: "20001"}, "2026-11-12")
	if !changed || m.Affinity == nil || *m.Affinity != InitialAffinity || m.LastInteractionDay != "2026-11-12" {
		t.Fatalf("first interaction = %+v (affinity %s), %v; want 20 on 2026-11-12", m, affinityOf(m), changed)
	}

	again, changed := ApplyInteraction(m, "2026-11-12")
	if changed || *again.Affinity != 20 {
		t.Fatalf("same day = %s, %v; want 20, false", affinityOf(again), changed)
	}

	next, changed := ApplyInteraction(again, "2026-11-13")
	if !changed || *next.Affinity != 21 || next.LastInteractionDay != "2026-11-13" {
		t.Fatalf("next day = %+v (affinity %s), %v; want 21", next, affinityOf(next), changed)
	}
	if *again.Affinity != 20 {
		t.Fatalf("input affinity changed to %d; want ApplyInteraction to leave it alone", *again.Affinity)
	}

	top, changed := ApplyInteraction(store.MemberMemory{Affinity: intPtr(100), LastInteractionDay: "2026-11-12"}, "2026-11-13")
	if !changed || *top.Affinity != 100 || top.LastInteractionDay != "2026-11-13" {
		t.Fatalf("100 next day = %+v (affinity %s), %v; want 100 with the day updated", top, affinityOf(top), changed)
	}

	idle, changed := ApplyInteraction(store.MemberMemory{Affinity: intPtr(30), LastInteractionDay: "2026-10-01", DecaySteps: 3},
		"2026-11-12")
	if !changed || idle.DecaySteps != 0 || *idle.Affinity != 31 {
		t.Fatalf("after decay = %+v (affinity %s), %v; want 31 with DecaySteps 0", idle, affinityOf(idle), changed)
	}
}

func TestApplyModelDelta(t *testing.T) {
	const day = "2026-11-12"
	if m, applied := ApplyModelDelta(store.MemberMemory{}, 3, day); applied != 0 || m.Affinity != nil {
		t.Fatalf("nil affinity = %s, applied %d; want unchanged, 0", affinityOf(m), applied)
	}

	if m, applied := ApplyModelDelta(store.MemberMemory{Affinity: intPtr(40)}, 7, day); applied != 3 || *m.Affinity != 43 {
		t.Fatalf("40 +7 = %s, applied %d; want 43, 3", affinityOf(m), applied)
	}
	if m, applied := ApplyModelDelta(store.MemberMemory{Affinity: intPtr(40)}, -9, day); applied != -3 || *m.Affinity != 37 {
		t.Fatalf("40 -9 = %s, applied %d; want 37, -3", affinityOf(m), applied)
	}

	m := store.MemberMemory{Affinity: intPtr(40)}
	var applied int
	for i, want := range []int{3, 2} {
		if m, applied = ApplyModelDelta(m, 3, day); applied != want {
			t.Fatalf("gain %d applied %d, want %d", i, applied, want)
		}
	}
	if *m.Affinity != 45 || m.ModelDeltaSum != 5 || m.ModelDeltaDay != day {
		t.Fatalf("after daily gain = %+v (affinity %s); want 45 with sum 5", m, affinityOf(m))
	}

	m = store.MemberMemory{Affinity: intPtr(40)}
	for i, want := range []int{-3, -3, -3, -1} {
		if m, applied = ApplyModelDelta(m, -3, day); applied != want {
			t.Fatalf("loss %d applied %d, want %d", i, applied, want)
		}
	}
	if *m.Affinity != 30 || m.ModelDeltaSum != -10 {
		t.Fatalf("after daily loss = %+v (affinity %s); want 30 with sum -10", m, affinityOf(m))
	}

	m, applied = ApplyModelDelta(m, -3, "2026-11-13")
	if applied != -3 || *m.Affinity != 27 || m.ModelDeltaSum != -3 || m.ModelDeltaDay != "2026-11-13" {
		t.Fatalf("next day = %+v (affinity %s), applied %d; want 27 with sum -3", m, affinityOf(m), applied)
	}

	if m, applied := ApplyModelDelta(store.MemberMemory{Affinity: intPtr(98)}, 3, day); applied != 2 || *m.Affinity != 100 ||
		m.ModelDeltaSum != 2 {
		t.Fatalf("98 +3 = %+v (affinity %s), applied %d; want 100, 2", m, affinityOf(m), applied)
	}
	if m, applied := ApplyModelDelta(store.MemberMemory{Affinity: intPtr(1)}, -3, day); applied != -1 || *m.Affinity != 0 ||
		m.ModelDeltaSum != -1 {
		t.Fatalf("1 -3 = %+v (affinity %s), applied %d; want 0, -1", m, affinityOf(m), applied)
	}

	in := store.MemberMemory{Affinity: intPtr(40)}
	ApplyModelDelta(in, 3, day)
	if *in.Affinity != 40 {
		t.Fatalf("input affinity changed to %d; want ApplyModelDelta to leave it alone", *in.Affinity)
	}
}

func TestDecay(t *testing.T) {
	const last = "2026-10-01"
	day := func(idle int) string {
		d, _ := time.Parse(time.DateOnly, last)
		return d.AddDate(0, 0, idle).Format(time.DateOnly)
	}
	m := store.MemberMemory{Affinity: intPtr(30), LastInteractionDay: last}

	if got, changed := Decay(m, day(20)); changed || *got.Affinity != 30 || got.DecaySteps != 0 {
		t.Fatalf("idle 20 = %+v (affinity %s), %v; want unchanged", got, affinityOf(got), changed)
	}

	m, changed := Decay(m, day(21))
	if !changed || *m.Affinity != 29 || m.DecaySteps != 1 {
		t.Fatalf("idle 21 = %+v (affinity %s), %v; want 29 with DecaySteps 1", m, affinityOf(m), changed)
	}
	if again, changed := Decay(m, day(21)); changed || *again.Affinity != 29 || again.DecaySteps != 1 {
		t.Fatalf("idle 21 again = %+v (affinity %s), %v; want unchanged", again, affinityOf(again), changed)
	}
	if m, changed = Decay(m, day(28)); !changed || *m.Affinity != 28 || m.DecaySteps != 2 {
		t.Fatalf("idle 28 = %+v (affinity %s), %v; want 28 with DecaySteps 2", m, affinityOf(m), changed)
	}

	in := store.MemberMemory{Affinity: intPtr(21), LastInteractionDay: last}
	if got, _ := Decay(in, day(35)); *got.Affinity != DecayFloor || got.DecaySteps != 3 {
		t.Fatalf("21 idle 35 = %+v (affinity %s); want 20 with DecaySteps 3", got, affinityOf(got))
	}
	if *in.Affinity != 21 {
		t.Fatalf("input affinity changed to %d; want Decay to leave it alone", *in.Affinity)
	}
	if got, _ := Decay(store.MemberMemory{Affinity: intPtr(15), LastInteractionDay: last}, day(60)); *got.Affinity != 15 {
		t.Fatalf("15 idle 60 = %s; want 15", affinityOf(got))
	}
	if got, changed := Decay(store.MemberMemory{Affinity: intPtr(30)}, day(60)); changed || *got.Affinity != 30 {
		t.Fatalf("no day = %s, %v; want unchanged", affinityOf(got), changed)
	}
	if got, changed := Decay(store.MemberMemory{LastInteractionDay: last}, day(60)); changed || got.Affinity != nil {
		t.Fatalf("nil affinity = %s, %v; want unchanged", affinityOf(got), changed)
	}
}

func TestLocalDay(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	if got := LocalDay(time.Date(2026, 11, 12, 15, 59, 0, 0, time.UTC), shanghai); got != "2026-11-12" {
		t.Errorf("15:59Z = %q, want 2026-11-12", got)
	}
	if got := LocalDay(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC), shanghai); got != "2026-11-13" {
		t.Errorf("16:00Z = %q, want 2026-11-13", got)
	}
	if got := LocalDay(time.Date(2026, 11, 12, 23, 59, 0, 0, shanghai), nil); got != "2026-11-12" {
		t.Errorf("nil location = %q, want the UTC date 2026-11-12", got)
	}
}
