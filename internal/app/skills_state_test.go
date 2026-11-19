package app

import (
	"maps"
	"testing"

	"github.com/kinako404/conloom/internal/skills"
)

func TestBuiltinTools_AllClassified(t *testing.T) {
	want := map[string]bool{
		"remember":     true,
		"recall":       false,
		"get_affinity": false,
		"now":          false,
		"set_status":   true,
		"set_scene":    true,
		"adjust_buff":  true,
		"add_buff":     true,
		"remove_buff":  true,
	}
	got := map[string]bool{}
	for _, tool := range builtinTools(toolDeps{}) {
		got[tool.Spec().Name] = skills.ChangesState(tool)
	}
	if !maps.Equal(got, want) {
		t.Errorf("built-in tools classified as %v, want %v", got, want)
	}
}
