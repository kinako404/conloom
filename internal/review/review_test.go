package review

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func basicPolicy() conloomapi.ReviewPolicy {
	return conloomapi.DefaultGroupPolicy().Review
}

func TestOutput_StripsCQCodes(t *testing.T) {
	r := New(nil, Options{})
	got := r.Output("好的[CQ:at,qq=all]大家", basicPolicy())
	if got.Text != "好的大家" || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v", got)
	}

	got = r.Output("a[C[CQ:face,id=1]Q:at,qq=all]b", basicPolicy())
	if got.Text != "ab" || got.Verdict != VerdictSanitize {
		t.Fatalf("nested CQ: got %+v", got)
	}
}

func TestOutput_AllowsCleanText(t *testing.T) {
	r := New([]string{"abc123secretvalue"}, Options{})
	got := r.Output("你好，今天天气不错", basicPolicy())
	if got.Text != "你好，今天天气不错" || got.Verdict != VerdictAllow || len(got.Reasons) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestOutput_RedactsSecrets(t *testing.T) {
	r := New([]string{"", "abc123secretvalue"}, Options{})
	got := r.Output("key=abc123secretvalue token sk-ABCDEFGHIJKLMNOPQRST end", basicPolicy())
	want := "key=[已隐藏] token [已隐藏] end"
	if got.Text != want || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v, want text %q", got, want)
	}

	got = r.Output("Authorization: Bearer abcdefghijklmnop.qrs and 0123456789abcdef0123456789ABCDEF!", basicPolicy())
	want = "Authorization: [已隐藏] and [已隐藏]!"
	if got.Text != want || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v, want text %q", got, want)
	}

	got = r.Output("short sk-abc and deadbeef", basicPolicy())
	if got.Text != "short sk-abc and deadbeef" || got.Verdict != VerdictAllow {
		t.Fatalf("short tokens must be kept: got %+v", got)
	}
}

func TestOutput_RedactsBase64Tokens(t *testing.T) {
	r := New(nil, Options{})
	cases := []struct{ in, want string }{
		{"key: QWxhZGRpbjpvcGVuIHNlc2FtZQ9xYm9vbGVhbg1+/Z== ok", "key: [已隐藏] ok"},
		{"auth Bearer ab+cdefghijklmnopqrstuv done", "auth [已隐藏] done"},
		{"word " + strings.Repeat("quick", 10) + " end", "word " + strings.Repeat("quick", 10) + " end"},
		{"今天天气很好，我们一起去公园散步吧，顺便买点水果回来。", "今天天气很好，我们一起去公园散步吧，顺便买点水果回来。"},
		{"The quick brown fox jumps over the lazy dog 42 times today.", "The quick brown fox jumps over the lazy dog 42 times today."},
	}
	for _, c := range cases {
		got := r.Output(c.in, basicPolicy())
		if got.Text != c.want {
			t.Errorf("Output(%q).Text = %q, want %q", c.in, got.Text, c.want)
		}
		wantVerdict := VerdictAllow
		if c.in != c.want {
			wantVerdict = VerdictSanitize
		}
		if got.Verdict != wantVerdict {
			t.Errorf("Output(%q).Verdict = %q, want %q", c.in, got.Verdict, wantVerdict)
		}
	}
}

func TestOutput_BlockedWordBlock(t *testing.T) {
	pol := basicPolicy()
	pol.Action = "block"
	pol.BlockedWords = []string{"坏词"}
	got := New(nil, Options{}).Output("这里有坏词出现", pol)
	if got.Verdict != VerdictBlock {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(got.Text, "坏词") {
		t.Fatalf("blocked text leaked: %q", got.Text)
	}
}

func TestOutput_BlockedWordSanitize(t *testing.T) {
	pol := basicPolicy()
	pol.Action = "sanitize"
	pol.BlockedWords = []string{"坏词"}
	got := New(nil, Options{}).Output("坏词", pol)
	if got.Text != "**" || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v", got)
	}

	got = New(nil, Options{}).Output("a坏词b坏词c", pol)
	if got.Text != "a**b**c" || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v", got)
	}
}

func TestOutput_BlockedWordAllow(t *testing.T) {
	pol := basicPolicy()
	pol.Action = "allow"
	pol.BlockedWords = []string{"坏词"}
	got := New(nil, Options{}).Output("有坏词", pol)
	if got.Text != "有坏词" || got.Verdict != VerdictAllow || !slices.Contains(got.Reasons, reasonBlockedWord) {
		t.Fatalf("got %+v", got)
	}
}

func TestOutput_EmptyAfterCleanBlocks(t *testing.T) {
	r := New(nil, Options{})
	for _, in := range []string{"[CQ:face,id=1]", "  [CQ:face,id=1]\n", "", "   "} {
		got := r.Output(in, basicPolicy())
		if got.Verdict != VerdictBlock {
			t.Fatalf("%q: got %+v", in, got)
		}
	}
}

func TestOutput_TruncatesByRunes(t *testing.T) {
	got := New(nil, Options{}).Output(strings.Repeat("😀", 2000), basicPolicy())
	if n := utf8.RuneCountInString(got.Text); n != 1500 {
		t.Fatalf("rune count = %d, want 1500", n)
	}
	if !utf8.ValidString(got.Text) || got.Verdict != VerdictSanitize {
		t.Fatalf("valid=%v verdict=%s", utf8.ValidString(got.Text), got.Verdict)
	}

	got = New(nil, Options{MaxOutputRunes: 3}).Output("一二三四", basicPolicy())
	if got.Text != "一二三" {
		t.Fatalf("custom limit: got %q", got.Text)
	}
}

func TestOutput_IgnoresReviewOutputFalse(t *testing.T) {
	pol := basicPolicy()
	pol.ReviewOutput = false
	got := New(nil, Options{}).Output("好的[CQ:at,qq=all]", pol)
	if got.Text != "好的" || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v", got)
	}
}

func TestInput_TruncatesByRunes(t *testing.T) {
	got := New(nil, Options{}).Input(strings.Repeat("中", 5000), basicPolicy())
	want := strings.Repeat("中", 4000) + truncationMarker
	if got.Text != want || !utf8.ValidString(got.Text) || got.Verdict != VerdictSanitize {
		t.Fatalf("len=%d verdict=%s", utf8.RuneCountInString(got.Text), got.Verdict)
	}

	pol := basicPolicy()
	pol.ReviewInput = false
	got = New(nil, Options{MaxInputRunes: 2}).Input("一二三", pol)
	if got.Text != "一二"+truncationMarker || got.Verdict != VerdictSanitize {
		t.Fatalf("custom limit with review_input=false: got %+v", got)
	}
}

func TestInput_RemovesControlChars(t *testing.T) {
	r := New(nil, Options{})
	got := r.Input("a\x00b\nc", basicPolicy())
	if got.Text != "ab\nc" || got.Verdict != VerdictSanitize {
		t.Fatalf("got %+v", got)
	}

	got = r.Input("x\ty\r\x1b\u0085z", basicPolicy())
	if got.Text != "x\tyz" {
		t.Fatalf("got %q", got.Text)
	}

	got = r.Input("普通\n消息", basicPolicy())
	if got.Text != "普通\n消息" || got.Verdict != VerdictAllow || len(got.Reasons) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestLevelImplemented(t *testing.T) {
	for level, want := range map[string]bool{"basic": true, "standard": false, "strict": false, "": false} {
		if got := LevelImplemented(level); got != want {
			t.Errorf("LevelImplemented(%q) = %v, want %v", level, got, want)
		}
	}
}
