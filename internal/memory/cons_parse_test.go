package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseOutput_ClampsAndTruncates(t *testing.T) {
	body := `{"group_summary":"群里在聊猫","members":[{"ref":"P1","note":"喜欢猫","affinity_delta":1,"reason":"友善"}]}`
	for name, content := range map[string]string{
		"plain":      body,
		"code block": "```json\n" + body + "\n```",
		"with prose": "好的，整理结果如下：\n" + body + "\n以上。",
	} {
		out, err := parseOutput(content)
		if err != nil {
			t.Fatalf("%s: parseOutput: %v", name, err)
		}
		want := modelOutput{Summary: "群里在聊猫", Members: []modelMember{{Ref: "P1", Note: "喜欢猫", Reason: "友善", Delta: 1}}}
		if out.Summary != want.Summary || len(out.Members) != 1 || out.Members[0] != want.Members[0] {
			t.Fatalf("%s: parseOutput = %+v, want %+v", name, out, want)
		}
	}

	long := `{"group_summary":"` + strings.Repeat("摘", 5000) + `","members":[
		{"ref":"P1","note":"` + strings.Repeat("记", 250) + `","affinity_delta":9,"reason":"` + strings.Repeat("因", 250) + `"},
		{"ref":"P2","affinity_delta":-9},
		{"ref":"P3","affinity_delta":1.6},
		{"ref":"P4","affinity_delta":"很多"},
		{"ref":"P5","affinity_delta":-1.5},
		{"ref":"P9","note":"未知代号"},
		{"ref":"P1","note":"重复的代号","affinity_delta":-3}
	]}`
	out, err := parseOutput(long)
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if n := utf8.RuneCountInString(out.Summary); n != SummaryMaxRunes {
		t.Errorf("summary runes = %d, want %d", n, SummaryMaxRunes)
	}
	if len(out.Members) != 6 {
		t.Fatalf("members = %+v, want 6 (duplicate P1 dropped, unknown P9 kept)", out.Members)
	}
	first := out.Members[0]
	if first.Ref != "P1" || utf8.RuneCountInString(first.Note) != NoteMaxRunes || utf8.RuneCountInString(first.Reason) != 200 || first.Delta != 3 {
		t.Errorf("P1 = note %d runes, reason %d runes, delta %d; want 200, 200, 3 (first P1 wins)",
			utf8.RuneCountInString(first.Note), utf8.RuneCountInString(first.Reason), first.Delta)
	}
	for i, want := range []struct {
		ref   string
		delta int
	}{{"P2", -3}, {"P3", 2}, {"P4", 0}, {"P5", -2}, {"P9", 0}} {
		if m := out.Members[i+1]; m.Ref != want.ref || m.Delta != want.delta {
			t.Errorf("member %d = %+v, want ref %s delta %d", i+1, m, want.ref, want.delta)
		}
	}
	if out.Members[5].Note != "未知代号" {
		t.Errorf("P9 note = %q, want kept for the writer to ignore", out.Members[5].Note)
	}

	for _, bad := range []string{"抱歉，我无法完成整理。", "", "{", "} {", `{"group_summary":`, `{"members":"P1"}`} {
		if out, err := parseOutput(bad); err == nil {
			t.Errorf("parseOutput(%q) = %+v, want error", bad, out)
		}
	}
}
