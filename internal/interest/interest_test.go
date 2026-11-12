package interest

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestTerms_CJKBigramsAndASCIIWords(t *testing.T) {
	got := Terms("我最近在玩原神和Minecraft")
	for _, want := range []string{"原神", "minecraft"} {
		if !slices.Contains(got, want) {
			t.Errorf("Terms() = %v, missing %q", got, want)
		}
	}
	for _, bad := range []string{"我最", "最近", "近在", "在玩", "神和", "Minecraft"} {
		if slices.Contains(got, bad) {
			t.Errorf("Terms() = %v, must not contain %q", got, bad)
		}
	}
}

func TestTerms_ASCIIWordRules(t *testing.T) {
	got := Terms("I play Elden-Ring a LOT, x y rpg")
	want := []string{"play", "elden", "ring", "lot", "rpg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms() = %v, want %v", got, want)
	}
}

func TestTerms_DropsStopwordsAndDedups(t *testing.T) {
	got := Terms("我们觉得什么时候都可以，哈哈哈 the and you this that what with lol 原神，原神 GENSHIN genshin")
	want := []string{"原神", "genshin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms() = %v, want %v", got, want)
	}
}

func TestTerms_EmptyAndPunctuation(t *testing.T) {
	for _, in := range []string{"", "   ", "？！。，", "哈哈", "a b c", "1234"} {
		if got := Terms(in); len(got) != 0 {
			t.Errorf("Terms(%q) = %v, want empty", in, got)
		}
	}
}

func TestUpdate_DecaysAndCaps(t *testing.T) {
	p := Update(nil, "原神")
	if p["原神"] != 1 {
		t.Fatalf("first update weight = %v, want 1", p["原神"])
	}
	p = Update(p, "原神 minecraft")
	if !approx(p["原神"], 1.9) || !approx(p["minecraft"], 1) {
		t.Fatalf("after second update = %v, want 原神=1.9 minecraft=1", p)
	}
	p = Update(p, "")
	if !approx(p["原神"], 1.71) || !approx(p["minecraft"], 0.9) {
		t.Fatalf("after empty update = %v, want pure decay", p)
	}

	full := map[string]float64{}
	for i := range MaxTerms {
		full[fmt.Sprintf("word%c%c", 'a'+i/26, 'a'+i%26)] = float64(i + 10)
	}
	got := Update(full, "newterm")
	if len(got) != MaxTerms {
		t.Fatalf("len = %d, want %d", len(got), MaxTerms)
	}
	if _, ok := got["newterm"]; ok {
		t.Fatalf("new term weighing 1 must lose to decayed weights >= 9: %v", got)
	}
	if !approx(got["wordaa"], 9) {
		t.Fatalf("wordaa = %v, want 9", got["wordaa"])
	}

	weak := map[string]float64{}
	for i := range MaxTerms {
		weak[fmt.Sprintf("word%c%c", 'a'+i/26, 'a'+i%26)] = 0.5
	}
	got = Update(weak, "newterm")
	if len(got) != MaxTerms {
		t.Fatalf("len = %d, want %d", len(got), MaxTerms)
	}
	if !approx(got["newterm"], 1) {
		t.Fatalf("newterm = %v, want 1", got["newterm"])
	}
	if _, ok := got["wordbx"]; ok {
		t.Fatalf("among equal weights the lexicographically last term is evicted: %v", got)
	}
	if !approx(got["wordaa"], 0.45) {
		t.Fatalf("wordaa = %v, want 0.45", got["wordaa"])
	}
}

func TestUpdate_DoesNotMutateInput(t *testing.T) {
	in := map[string]float64{"原神": 2, "minecraft": 1}
	snapshot := map[string]float64{"原神": 2, "minecraft": 1}
	out := Update(in, "原神 genshin")
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("input mutated: %v", in)
	}
	out["x"] = 1
	if _, ok := in["x"]; ok {
		t.Fatal("output aliases input")
	}
}

func TestTop_OrderAndTies(t *testing.T) {
	p := map[string]float64{"bb": 2, "aa": 2, "cc": 3, "dd": 1}
	if got, want := Top(p, 10), []string{"cc", "aa", "bb", "dd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Top(10) = %v, want %v", got, want)
	}
	if got, want := Top(p, 2), []string{"cc", "aa"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Top(2) = %v, want %v", got, want)
	}
	if got := Top(p, 0); len(got) != 0 {
		t.Fatalf("Top(0) = %v, want empty", got)
	}
	if got := Top(nil, 5); len(got) != 0 {
		t.Fatalf("Top(nil) = %v, want empty", got)
	}
}

func rankedProfile() map[string]float64 {
	p := map[string]float64{}
	for i := range MaxTerms {
		p[fmt.Sprintf("term%c%c", 'a'+i/26, 'a'+i%26)] = float64(100 - i)
	}
	return p
}

func TestMatch_Levels(t *testing.T) {
	p := rankedProfile()

	one := "termaa 随便聊聊"
	if m, ok := Match(p, one, "low"); ok || m != nil {
		t.Errorf("low with 1 shared = %v,%v, want nil,false", m, ok)
	}
	if m, ok := Match(p, one, "mid"); !ok || !reflect.DeepEqual(m, []string{"termaa"}) {
		t.Errorf("mid with 1 shared = %v,%v, want [termaa],true", m, ok)
	}

	two := "termab and termaa"
	if m, ok := Match(p, two, "low"); !ok || !reflect.DeepEqual(m, []string{"termab", "termaa"}) {
		t.Errorf("low with 2 shared = %v,%v, want [termab termaa],true", m, ok)
	}

	rank40 := "termbn"
	if got := Top(p, 40)[39]; got != rank40 {
		t.Fatalf("fixture: rank 40 = %q, want %q", got, rank40)
	}
	if _, ok := Match(p, rank40, "mid"); ok {
		t.Errorf("mid with rank-40 term matched")
	}
	if m, ok := Match(p, rank40, "high"); !ok || !reflect.DeepEqual(m, []string{rank40}) {
		t.Errorf("high with rank-40 term = %v,%v, want [%s],true", m, ok, rank40)
	}

	rank25 := Top(p, 25)[24]
	if _, ok := Match(p, rank25+" "+rank40, "low"); ok {
		t.Errorf("low must only consider top 20")
	}

	for _, lvl := range []string{"off", "", "HIGH", "max"} {
		if m, ok := Match(p, two, lvl); ok || m != nil {
			t.Errorf("Match level %q = %v,%v, want nil,false", lvl, m, ok)
		}
	}
}

func TestMatch_EmptyProfile(t *testing.T) {
	for _, lvl := range []string{"low", "mid", "high"} {
		if m, ok := Match(nil, "原神 minecraft", lvl); ok || m != nil {
			t.Errorf("nil profile %s = %v,%v", lvl, m, ok)
		}
		if m, ok := Match(map[string]float64{}, "原神", lvl); ok || m != nil {
			t.Errorf("empty profile %s = %v,%v", lvl, m, ok)
		}
	}
	if m, ok := Match(map[string]float64{"原神": 1}, "", "high"); ok || m != nil {
		t.Errorf("empty text = %v,%v", m, ok)
	}
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
