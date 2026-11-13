package skills

import "testing"

type limitedTool struct {
	fakeTool
	limit int
}

func (l limitedTool) MaxResultRunes() int { return l.limit }

func TestMaxResultRunes(t *testing.T) {
	for _, c := range []struct {
		tool Tool
		want int
	}{
		{fakeTool{"now"}, DefaultMaxResultRunes},
		{limitedTool{fakeTool{"recall"}, 1500}, 1500},
		{limitedTool{fakeTool{"x"}, 0}, DefaultMaxResultRunes},
		{limitedTool{fakeTool{"x"}, 5000}, DefaultMaxResultRunes},
	} {
		if got := MaxResultRunes(c.tool); got != c.want {
			t.Errorf("MaxResultRunes(%s) = %d, want %d", c.tool.Spec().Name, got, c.want)
		}
	}
	set := Toolset{Tools: []Tool{limitedTool{fakeTool{"now"}, 100}, limitedTool{fakeTool{"recall"}, 1500}}}
	if got := set.MaxResultRunes(); got != 1500 {
		t.Errorf("Toolset.MaxResultRunes = %d, want 1500", got)
	}
	if got := (Toolset{}).MaxResultRunes(); got != 0 {
		t.Errorf("empty Toolset.MaxResultRunes = %d, want 0", got)
	}
}
