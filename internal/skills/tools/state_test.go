package tools

import (
	"testing"

	"github.com/kinako404/conloom/internal/skills"
)

func TestBuiltinTools_ChangesState(t *testing.T) {
	for _, c := range []struct {
		tool skills.Tool
		want bool
	}{
		{NewRemember(RememberDeps{}), true},
		{NewSetStatus(nil, nil, nil, nil), true},
		{NewSetScene(nil, nil, nil, nil), true},
		{NewAdjustBuff(nil, nil), true},
		{NewAddBuff(nil, nil, nil, nil), true},
		{NewRemoveBuff(nil, nil), true},
		{NewRecall(RecallDeps{}), false},
		{NewGetAffinity(AffinityDeps{}), false},
		{NewNow(), false},
	} {
		if got := skills.ChangesState(c.tool); got != c.want {
			t.Errorf("ChangesState(%s) = %v, want %v", c.tool.Spec().Name, got, c.want)
		}
	}
}
