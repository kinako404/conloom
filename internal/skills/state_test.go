package skills

import (
	"slices"
	"testing"
)

type stateTool struct {
	fakeTool
	changes bool
}

func (s stateTool) ChangesState() bool { return s.changes }

func TestChangesState(t *testing.T) {
	for _, c := range []struct {
		tool Tool
		want bool
	}{
		{fakeTool{"now"}, false},
		{stateTool{fakeTool{"maybe"}, false}, false},
		{stateTool{fakeTool{"set_status"}, true}, true},
	} {
		if got := ChangesState(c.tool); got != c.want {
			t.Errorf("ChangesState(%s) = %v, want %v", c.tool.Spec().Name, got, c.want)
		}
	}
}

func toolNames(ts []Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Spec().Name)
	}
	return out
}

func TestToolset_LookupsAndStateTools(t *testing.T) {
	set := Toolset{Section: "S", Tools: []Tool{
		fakeTool{"a"},
		stateTool{fakeTool{"b"}, true},
		stateTool{fakeTool{"c"}, false},
	}}
	look := set.Lookups()
	if look.Section != "S" || !slices.Equal(toolNames(look.Tools), []string{"a", "c"}) {
		t.Errorf("Lookups = {%q, %v}, want {S, [a c]}", look.Section, toolNames(look.Tools))
	}
	st := set.StateTools()
	if st.Section != "S" || !slices.Equal(toolNames(st.Tools), []string{"b"}) {
		t.Errorf("StateTools = {%q, %v}, want {S, [b]}", st.Section, toolNames(st.Tools))
	}
	if len(set.Tools) != 3 || set.Tools[1].Spec().Name != "b" {
		t.Errorf("splitting changed the original toolset: %v", toolNames(set.Tools))
	}

	lookupsOnly := Toolset{Section: "S", Tools: []Tool{fakeTool{"a"}, fakeTool{"c"}}}
	if st := lookupsOnly.StateTools(); !st.Empty() || st.Section != "S" {
		t.Errorf("all-lookup StateTools = {%q, %v}, want empty with Section S", st.Section, toolNames(st.Tools))
	}
	if look := lookupsOnly.Lookups(); look.Section != "S" || !slices.Equal(toolNames(look.Tools), []string{"a", "c"}) {
		t.Errorf("all-lookup Lookups = {%q, %v}, want {S, [a c]}", look.Section, toolNames(look.Tools))
	}

	var zero Toolset
	if look := zero.Lookups(); look.Section != "" || look.Tools != nil {
		t.Errorf("zero Lookups = %+v, want zero", look)
	}
	if st := zero.StateTools(); st.Section != "" || st.Tools != nil {
		t.Errorf("zero StateTools = %+v, want zero", st)
	}
}
