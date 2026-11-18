package tools

import (
	"testing"

	"github.com/kinako404/conloom/internal/skills"
)

func TestTools_DeclareResultLimits(t *testing.T) {
	for _, c := range []struct {
		tool skills.Tool
		want int
	}{
		{NewRecall(RecallDeps{}), recallMaxRunes},
		{NewRemember(RememberDeps{}), shortResultRunes},
		{NewGetAffinity(AffinityDeps{}), shortResultRunes},
		{NewNow(), shortResultRunes},
		{NewSetStatus(nil, nil, nil, nil), shortResultRunes},
		{NewSetScene(nil, nil, nil, nil), shortResultRunes},
		{NewAdjustBuff(nil, nil), shortResultRunes},
	} {
		if got := skills.MaxResultRunes(c.tool); got != c.want {
			t.Errorf("%s: MaxResultRunes = %d, want %d", c.tool.Spec().Name, got, c.want)
		}
	}
	if recallMaxRunes != 1500 || shortResultRunes > 100 {
		t.Errorf("limits = %d / %d", recallMaxRunes, shortResultRunes)
	}
}
