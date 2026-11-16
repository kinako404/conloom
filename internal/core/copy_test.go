package core

import (
	"strings"
	"testing"
)

func TestCopyDirectiveAffinity(t *testing.T) {
	protected := map[string]string{
		"陌生":  "保持礼貌和距离",
		"认识":  "友好",
		"熟悉":  "开玩笑",
		"亲近":  "信任",
		"很要好": "依赖",
	}
	for tier, want := range protected {
		got := CopyDirectiveAffinity(tier, false)
		if !strings.HasPrefix(got, "【关系提示】") || !strings.Contains(got, want) {
			t.Errorf("protected %s = %q, want prefix and %q", tier, got, want)
		}
		if strings.Contains(got, "内容规则") || strings.Contains(got, "对方明确主动") {
			t.Errorf("protected %s carries open-mode wording: %q", tier, got)
		}
	}

	open := map[string]string{
		"陌生":  "保持克制",
		"认识":  "轻度亲密互动",
		"熟悉":  "拥抱、牵手",
		"亲近":  "回应更亲密的互动",
		"很要好": "更主动、更深入地回应",
	}
	for tier, want := range open {
		got := CopyDirectiveAffinity(tier, true)
		if !strings.HasPrefix(got, "【关系提示】") || !strings.Contains(got, want) {
			t.Errorf("open %s = %q, want prefix and %q", tier, got, want)
		}
	}
	for _, tier := range []string{"亲近", "很要好"} {
		if got := CopyDirectiveAffinity(tier, true); !strings.Contains(got, "内容规则") {
			t.Errorf("open %s = %q, want it to defer to the persona's content rules", tier, got)
		}
	}
	if got := CopyDirectiveAffinity("unknown", true); got != "" {
		t.Errorf("unknown tier = %q, want empty", got)
	}
}
