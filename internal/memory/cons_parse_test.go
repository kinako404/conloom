package memory

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/pkg/conloomapi"
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

	long := `{"group_summary":"` + strings.Repeat("摘", 2*conloomapi.MaxSummaryMaxChars+1000) + `","members":[
		{"ref":"P1","note":"` + strings.Repeat("记", 350) + `","affinity_delta":9,"reason":"` + strings.Repeat("因", 250) + `"},
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
	if n := utf8.RuneCountInString(out.Summary); n != consRawMaxRunes {
		t.Errorf("summary runes = %d, want %d (only the wide raw limit)", n, consRawMaxRunes)
	}
	if len(out.Members) != 6 {
		t.Fatalf("members = %+v, want 6 (duplicate P1 dropped, unknown P9 kept)", out.Members)
	}
	first := out.Members[0]
	if first.Ref != "P1" || utf8.RuneCountInString(first.Note) != 350 || utf8.RuneCountInString(first.Reason) != 250 || first.Delta != 3 {
		t.Errorf("P1 = note %d runes, reason %d runes, delta %d; want 350, 250 (not cut yet), 3 (first P1 wins)",
			utf8.RuneCountInString(first.Note), utf8.RuneCountInString(first.Reason), first.Delta)
	}
	stored := consResolveRefs(out, nil, nil, SummaryMaxRunes, NoteMaxRunes)
	if s, n, r := utf8.RuneCountInString(stored.Summary), utf8.RuneCountInString(stored.Members[0].Note),
		utf8.RuneCountInString(stored.Members[0].Reason); s != SummaryMaxRunes || n != NoteMaxRunes || r != consReasonMaxRunes {
		t.Errorf("resolved = summary %d, note %d, reason %d runes; want %d, %d, %d", s, n, r, SummaryMaxRunes, NoteMaxRunes, consReasonMaxRunes)
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

func TestConsTruncate_PrefersSentenceEnd(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"短句。", 10, "短句。"},
		{"第一句。第二句很长很长", 8, "第一句。"},
		{"问吗？答；还有很多字", 7, "问吗？答；"},
		{"惊！叹。", 3, "惊！"},
		{"没有句末标点的一长串字", 5, "没有句末标"},
		{"逗号，不算，句末", 6, "逗号，不算，"},
		{"a.b.c.d", 3, "a.b"},
		{"一二三四五六。七八九十甲乙", 12, "一二三四五六。"},
		{"嗯。" + strings.Repeat("长", 250), 200, "嗯。" + strings.Repeat("长", 198)},
		{"一二。三四五六七八九十", 7, "一二。三四五六"},
		{"一二。三四五六七八九十", 6, "一二。"},
	} {
		if got := consTruncate(c.in, c.max); got != c.want {
			t.Errorf("consTruncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestConsReplaceRefs(t *testing.T) {
	names := map[string]string{"P1": "小明", "P2": "小红", "P3": "一位群友"}
	for _, c := range []struct{ in, want string }{
		{"小明 （P1）喜欢猫", "小明喜欢猫"},
		{"小明\u3000(P1)喜欢猫", "小明喜欢猫"},
		{"小明（P1）喜欢猫", "小明喜欢猫"},
		{"小明(P1)喜欢猫", "小明喜欢猫"},
		{"小明（ P1 ）喜欢猫", "小明喜欢猫"},
		{"P1（小明）喜欢猫", "小明喜欢猫"},
		{"P1(小明)喜欢猫", "小明喜欢猫"},
		{"P1喜欢猫，P2喜欢狗", "小明喜欢猫，小红喜欢狗"},
		{"P1和P2聊天", "小明和小红聊天"},
		{"P12和P1", "P12和小明"},
		{"P9提到P1", "P9提到小明"},
		{"他（P2）说", "他（小红）说"},
		{"P1（昨天）说", "小明（昨天）说"},
		{"MP1 不是代号，P1 是", "MP1 不是代号，小明 是"},
		{"P1P2", "P1P2"},
		{"（P1）开头", "（小明）开头"},
		{"P3 说了话", "一位群友 说了话"},
		{"没有代号", "没有代号"},
		{"", ""},
	} {
		if got := consReplaceRefs(c.in, names, nil); got != c.want {
			t.Errorf("consReplaceRefs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConsReplaceRefs_LeavesCodesSeenInTheInput(t *testing.T) {
	names := map[string]string{"P1": "小明", "P2": "小红", "P5": "一位群友"}
	literal := consLiteralCodes("小红最近在玩P5，很喜欢", "P1 级别的bug", "1P和P2对战", "P12 不算")
	if want := map[string]bool{"P5": true, "P1": true, "P2": true, "P12": true}; len(literal) != len(want) ||
		!literal["P5"] || !literal["P1"] || !literal["P2"] || !literal["P12"] {
		t.Fatalf("consLiteralCodes = %v, want %v", literal, want)
	}
	for _, c := range []struct{ in, want string }{
		{"小红最近在玩P5，很喜欢", "小红最近在玩P5，很喜欢"},
		{"P1 级别的bug", "P1 级别的bug"},
		{"1P和P2对战", "1P和P2对战"},
		{"小明（P1）修了P1 级别的bug", "小明修了P1 级别的bug"},
		{"P2（小红）在玩P5", "小红在玩P5"},
		{"他（P2）说", "他（小红）说"},
	} {
		if got := consReplaceRefs(c.in, names, literal); got != c.want {
			t.Errorf("consReplaceRefs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := consReplaceRefs("P1和P2", names, map[string]bool{"P1": true}); got != "P1和小红" {
		t.Errorf("only P1 seen in the input: %q, want P2 still replaced", got)
	}
}

func TestConsResolveRefs_TruncatesAfterReplacing(t *testing.T) {
	names := map[string]string{"P1": "名字很长的小明同学"}
	note := strings.Repeat("猫", 280) + "。" + strings.Repeat("P1、", 9)
	summary := strings.Repeat("狗", 1490) + "。" + "P1、P1"
	out := consResolveRefs(modelOutput{Summary: summary, Members: []modelMember{
		{Ref: "P1", Note: note, Reason: "P1 很友善"},
		{Ref: "P9", Note: "P1 和 P9"},
	}}, names, nil, SummaryMaxRunes, NoteMaxRunes)
	if want := strings.Repeat("猫", 280) + "。"; out.Members[0].Note != want {
		t.Errorf("note = %q (%d runes), want cut at the sentence end", out.Members[0].Note, utf8.RuneCountInString(out.Members[0].Note))
	}
	if n := utf8.RuneCountInString(out.Summary); n > SummaryMaxRunes || strings.Contains(out.Summary, "P1") {
		t.Errorf("summary has %d runes or a code: %q", n, out.Summary[len(out.Summary)-30:])
	}
	if out.Members[0].Reason != "名字很长的小明同学 很友善" {
		t.Errorf("reason = %q", out.Members[0].Reason)
	}
	if out.Members[1].Note != "名字很长的小明同学 和 P9" || out.Members[1].Ref != "P9" {
		t.Errorf("unknown member = %+v, want only the known code replaced", out.Members[1])
	}
}

func TestParseOutput_LongSummaryKeepsCodesWholeForReplacement(t *testing.T) {
	names := map[string]string{"P1": "小明", "P12": "小红"}
	raw := strings.Repeat("狗", conloomapi.MaxSummaryMaxChars-2) + "P12"
	out, err := parseOutput(`{"group_summary":"` + raw + `","members":[]}`)
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	got := consResolveRefs(out, names, nil, conloomapi.MaxSummaryMaxChars, NoteMaxRunes).Summary
	if want := strings.Repeat("狗", conloomapi.MaxSummaryMaxChars-2) + "小红"; got != want {
		t.Fatalf("summary ends %q, want P12 replaced whole by its name", string([]rune(got)[conloomapi.MaxSummaryMaxChars-6:]))
	}
}
