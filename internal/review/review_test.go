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

func TestOutput_BlockedWordsMatchFolded(t *testing.T) {
	r := New(nil, Options{})
	pol := basicPolicy()
	pol.BlockedWords = []string{"坏词", "BadWord", ""}
	cases := []struct{ in, sanitized string }{
		{"这里有坏\u200b词出现", "这里有**出现"},
		{"a坏\ufeff词b坏词c", "a**b**c"},
		{"say ＢＡＤＷＯＲＤ now", "say ******* now"},
		{"say badword now", "say ******* now"},
		{"bad\u2060word, BADWORD!", "*******, *******!"},
		{"ba\u0301dword", "*******"},
	}
	for _, c := range cases {
		pol.Action = VerdictBlock
		if got := r.Output(c.in, pol); got.Verdict != VerdictBlock || got.Text != "" {
			t.Errorf("block Output(%q) = %+v, want it blocked", c.in, got)
		}
		pol.Action = VerdictSanitize
		if got := r.Output(c.in, pol); got.Text != c.sanitized || got.Verdict != VerdictSanitize ||
			!slices.Contains(got.Reasons, reasonBlockedWord) {
			t.Errorf("sanitize Output(%q) = %+v, want %q", c.in, got, c.sanitized)
		}
	}

	// A compatibility form folding to more than one letter is masked whole;
	// invalid UTF-8 is matched around.
	pol.BlockedWords, pol.Action = []string{"fi", "f"}, VerdictSanitize
	if got := r.Output("\ufb01ne \ufb00", pol); got.Text != "**ne *" {
		t.Errorf("sanitize of ligatures = %q, want %q", got.Text, "**ne *")
	}
	pol.BlockedWords = []string{"坏词"}
	if got := r.Output("坏\xff坏词\xff", pol); got.Text != "坏\xff**\xff" {
		t.Errorf("sanitize around invalid UTF-8 = %q", got.Text)
	}

	// Words stay apart: white space and punctuation are not dropped.
	pol.BlockedWords, pol.Action = []string{"hell", "坏词"}, VerdictBlock
	for _, in := range []string{"the llama", "坏，词", "ok"} {
		if got := r.Output(in, pol); got.Verdict != VerdictAllow || got.Text != in {
			t.Errorf("Output(%q) = %+v, want it allowed", in, got)
		}
	}
}

func TestOutput_BlockedWordsKeepKanaAndHangulWhole(t *testing.T) {
	r := New(nil, Options{})
	pol := basicPolicy()
	for _, c := range []struct{ word, in string }{
		{"ばか", "はかる時間がない"},
		{"バカ", "ハカセ"},
		{"가", "각자"},
		{"바보", "바봉"},
		{"한", "하나"},
	} {
		pol.BlockedWords = []string{c.word}
		for _, action := range []string{VerdictBlock, VerdictSanitize} {
			pol.Action = action
			if got := r.Output(c.in, pol); got.Verdict != VerdictAllow || got.Text != c.in {
				t.Errorf("%s %q in Output(%q) = %+v, want it allowed", action, c.word, c.in, got)
			}
		}
	}

	pol.Action = VerdictSanitize
	for _, c := range []struct{ word, in, sanitized string }{
		{"ばか", "それはばかだ", "それは**だ"},
		{"ばか", "それは\u306f\u3099かだ", "それは**だ"},
		{"バカ", "ﾊﾞｶだ", "**だ"},
		{"바보", "너는 바보야", "너는 **야"},
		{"바보", "너는 바\u200b보야", "너는 **야"},
		{"ｂａｄ", "so BAD", "so ***"},
		{"cafe", "Ca\ufe0ffé au lait", "**** au lait"},
	} {
		pol.BlockedWords = []string{c.word}
		if got := r.Output(c.in, pol); got.Text != c.sanitized || got.Verdict != VerdictSanitize {
			t.Errorf("sanitize %q in Output(%q) = %+v, want %q", c.word, c.in, got, c.sanitized)
		}
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
