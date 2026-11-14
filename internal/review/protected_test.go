package review

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const protectedWordsFile = "protected_words.b64"

func lexiconEntries(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(protectedWordsFile)
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		b, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		words = append(words, string(b))
	}
	return words
}

func toFullWidth(s string) string {
	return strings.Map(func(c rune) rune {
		if '!' <= c && c <= '~' {
			return c + 0xFEE0
		}
		return c
	}, s)
}

func mixedCase(s string) string {
	var b strings.Builder
	for i, c := range []rune(s) {
		if i%2 == 0 {
			c = unicode.ToUpper(c)
		}
		b.WriteRune(c)
	}
	return b.String()
}

func joinRunes(s, sep string) string {
	parts := strings.Split(s, "")
	return strings.Join(parts, sep)
}

func lexiconVariants(w string) []string {
	vs := []string{
		w,
		strings.ToUpper(w),
		mixedCase(w),
		toFullWidth(w),
		toFullWidth(mixedCase(w)),
		joinRunes(w, " "),
		joinRunes(w, "\u3000"),
		joinRunes(w, " \t\n "),
		joinRunes(toFullWidth(mixedCase(w)), " "),
		"好的，" + w + "。",
		"ok " + joinRunes(mixedCase(w), " ") + " ok",
	}
	for _, sep := range []string{"·", "・", ".", "_", "-", "*", "\u200b", "\u200c", "\u200d", "\u2060", "\ufeff", "．", "＿", "－", "＊",
		"\ufe0f", "\ufe0e", "\U000e0100", "\u0301", "\u0332", "\u20dd"} {
		vs = append(vs, joinRunes(w, sep))
	}
	return vs
}

func TestProtectedLexicon_FileIsBase64Only(t *testing.T) {
	data, err := os.ReadFile(protectedWordsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("%s must be non-empty and end with a newline", protectedWordsFile)
	}
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		b, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil {
			t.Fatalf("line %d is not base64: %v", i+1, err)
		}
		if base64.StdEncoding.EncodeToString(b) != line {
			t.Fatalf("line %d is not canonical base64", i+1)
		}
		w := string(b)
		if !utf8.ValidString(w) || strings.TrimSpace(w) == "" || w != strings.TrimSpace(w) {
			t.Fatalf("line %d does not decode to a single trimmed UTF-8 entry", i+1)
		}
		if seen[w] {
			t.Fatalf("line %d is a duplicate", i+1)
		}
		seen[w] = true
	}
	if len(protectedWords) != len(seen) {
		t.Fatalf("loaded %d entries, file has %d", len(protectedWords), len(seen))
	}
}

func TestProtectedLexicon_NoPlaintextInSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git work tree")
	}
	root := strings.TrimSpace(string(top))
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	words := lexiconEntries(t)
	checked := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		checked++
		lower := bytes.ToLower(data)
		for i, w := range words {
			if bytes.Contains(lower, []byte(strings.ToLower(w))) {
				t.Errorf("%s contains plaintext lexicon entry #%d", name, i+1)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no files checked")
	}
}

func TestOutputProtected_BlocksLexiconVariants(t *testing.T) {
	r := New(nil, Options{})
	words := lexiconEntries(t)
	if len(words) < 20 {
		t.Fatalf("lexicon has %d entries", len(words))
	}
	var han, latin int
	for _, w := range words {
		switch {
		case strings.IndexFunc(w, func(c rune) bool { return unicode.Is(unicode.Han, c) }) >= 0:
			han++
		case strings.IndexFunc(w, isASCIILetter) >= 0:
			latin++
		}
	}
	if han < 10 || latin < 10 {
		t.Fatalf("lexicon has %d Chinese and %d English entries", han, latin)
	}
	for i, w := range words {
		for j, v := range lexiconVariants(w) {
			got := r.OutputProtected(v, basicPolicy())
			if got.Verdict != VerdictBlock || got.Text != "" || !slices.Contains(got.Reasons, ReasonProtectedContent) {
				t.Errorf("entry #%d variant #%d not blocked: verdict=%q reasons=%v", i+1, j, got.Verdict, got.Reasons)
			}
		}
	}
}

func TestOutputProtected_BlocksWordSplitByCQCode(t *testing.T) {
	r := New(nil, Options{})
	for i, w := range lexiconEntries(t) {
		_, size := utf8.DecodeRuneInString(w)
		in := w[:size] + "[CQ:face,id=1]" + w[size:]
		got := r.OutputProtected(in, basicPolicy())
		if got.Verdict != VerdictBlock || got.Text != "" || !slices.Contains(got.Reasons, ReasonProtectedContent) {
			t.Errorf("entry #%d split by CQ code not blocked: %+v", i+1, got)
		}
	}
}

func TestOutputProtected_BlocksWordSplitAcrossReplyParts(t *testing.T) {
	r := New(nil, Options{})
	for i, w := range lexiconEntries(t) {
		_, size := utf8.DecodeRuneInString(w)
		if size == len(w) {
			continue
		}
		for j, sep := range []string{"\n|||\n", "|||", "\n｜｜｜\n"} {
			in := "好的" + w[:size] + sep + w[size:] + "。"
			got := r.OutputProtected(in, basicPolicy())
			if got.Verdict != VerdictBlock || got.Text != "" || !slices.Contains(got.Reasons, ReasonProtectedContent) {
				t.Errorf("entry #%d split by part separator #%d not blocked: verdict=%q reasons=%v", i+1, j, got.Verdict, got.Reasons)
			}
		}
	}
}

func TestOutputProtected_IgnoresPolicy(t *testing.T) {
	r := New(nil, Options{})
	w := lexiconEntries(t)[0]
	pols := []conloomapi.ReviewPolicy{
		{},
		{Level: "basic", ReviewOutput: false, Action: "sanitize"},
		{Level: "strict", ReviewOutput: true, Action: "block"},
		{Action: "sanitize", BlockedWords: []string{w}},
		{Action: "allow", BlockedWords: []string{"坏词"}},
	}
	for i, pol := range pols {
		got := r.OutputProtected("前面"+w+"后面", pol)
		if got.Verdict != VerdictBlock || got.Text != "" || !slices.Equal(got.Reasons, []string{ReasonProtectedContent}) {
			t.Errorf("policy #%d: got %+v", i, got)
		}
	}
}

var dailySentences = []string{
	"中午吃了什么呀？我吃了一碗牛肉面，还加了个荷包蛋。",
	"晚上想去吃火锅，你要不要一起？",
	"今天早饭只喝了一杯豆浆，现在有点饿了。",
	"乳酸菌饮料和酸奶哪个更适合早上喝？",
	"这家店的春卷和春饼都很好吃。",
	"今天天气不错，阳光很好，适合出去走走。",
	"明天要下大雨，记得带伞哦。",
	"降温了，晚上睡觉别着凉，多盖一床被子。",
	"春天到了，药店门口的花都开了。",
	"昨晚打游戏打到两点，排位终于上分了。",
	"这个副本的boss太难了，我们队灭了好几次。",
	"新出的手游你玩了吗？画面挺好看的。",
	"Let's play a round of chess after dinner, okay?",
	"今天加班到九点，老板又改需求了。",
	"周报写完了，下午还要开一个很长的会。",
	"这个项目下周上线，测试那边还在跑回归。",
	"I have a meeting with my manager at three, then I'm free.",
	"头有点疼，可能是昨晚没睡好。",
	"我感冒了，嗓子疼，还有点发烧。",
	"肚子不舒服，可能是吃坏东西了，自我安慰一下就当减肥吧。",
	"明天要去医院复查，医生说要空腹抽血。",
	"在窗口排队挂号排了半个小时，终于看上病了。",
	"医生给我开了点药，说一天吃三次，饭后吃。",
	"I've been coughing all week, so I'm going to see a doctor tomorrow.",
	"我家猫今天把花瓶打翻了，气死我了。",
	"带狗狗去打疫苗，它一路上都在发抖。",
	"小鸡仔和小鸭子在院子里跑来跑去，好可爱。",
	"My dog loves chasing the ball in the park every evening.",
	"小明，晚安，明天见！",
	"The analysis of the data shows sales grew this quarter.",
	"我加了一个母乳交流群，里面的妈妈们都很热情。",
	"冰箱里的牛乳交给你保管，记得早点喝完。",
	"豆乳交给前台就行。",
	"BOSS血太厚了，大家节约炮弹，等它残血再打。",
	"弹药不多了，节约炮火，先撤到掩体后面。",
	"这部剧讲的是古罗马女性奴隶的故事。",
	"猫主子太任性奴才只能听话。",
	"他从小就很任性 奴役别人可不行。",
	"今年春 药店里的口罩都卖光了。",
	"早春 药材的价格又涨了。",
	"立春 药膳汤要多喝点。",
}

func TestOutputProtected_AllowsDailyChat(t *testing.T) {
	if len(dailySentences) < 30 {
		t.Fatalf("only %d daily sentences", len(dailySentences))
	}
	r := New(nil, Options{})
	for _, s := range dailySentences {
		got := r.OutputProtected(s, basicPolicy())
		if got.Verdict != VerdictAllow || got.Text != s || len(got.Reasons) != 0 {
			t.Errorf("OutputProtected(%q) = %+v", s, got)
		}
	}
}

// crossWordSentencesB64 are ordinary sentences, base64-encoded, in which
// spans across word boundaries spell what would be lexicon-like entries once
// white space and separators are dropped.
var crossWordSentencesB64 = []string{
	"5a2m5qCh55qE5pep5pON6YC85b6X5oiR5aSp5aSp5YWt54K56LW35bqK44CC",
	"6L+Z5aWX5L2T5pON6YC855yf5Zyw5qih5Lu/5LqG5Yqo54mp55qE5Yqo5L2c44CC",
	"SGUgdG9vayBhIHN0YWdlaGFuZCBqb2IgYXQgdGhlIHRoZWF0ZXIgbGFzdCBzdW1tZXIu",
	"TXkgY291c2luIGZvdW5kIGEgZmFybWhhbmQgam9iIHVwIG5vcnRoLg==",
	"Q2xlYW5pbmcgdGhlIGRyYWlucyBpcyBhIGdyaW0gam9iLCBidXQgc29tZW9uZSBoYXMgdG8gZG8gaXQu",
	"Rml4ZWQgdGhlIHdoZWVsIHJpbS4gSm9iIGRvbmUh",
}

func TestOutputProtected_AllowsPhrasesAcrossWordBoundaries(t *testing.T) {
	r := New(nil, Options{})
	for i, enc := range crossWordSentencesB64 {
		b, err := base64.StdEncoding.Strict().DecodeString(enc)
		if err != nil {
			t.Fatalf("sentence #%d: %v", i+1, err)
		}
		s := string(b)
		if got := r.OutputProtected(s, basicPolicy()); got.Verdict != VerdictAllow || got.Text != s || len(got.Reasons) != 0 {
			t.Errorf("sentence #%d: verdict=%q reasons=%v", i+1, got.Verdict, got.Reasons)
		}
	}
}

func TestNormalizeProtected_KeepsWordSeparatingPunctuation(t *testing.T) {
	if got, want := normalizeProtected("A/b~c、d ❤\ufe0f e\u0301"), "a/b~c、d❤e"; got != want {
		t.Errorf("normalizeProtected = %q, want %q", got, want)
	}
}

func TestOutputProtected_FallsThroughToOutput(t *testing.T) {
	r := New([]string{"abc123secretvalue"}, Options{MaxOutputRunes: 10})
	block := basicPolicy()
	block.Action = "block"
	block.BlockedWords = []string{"坏词"}
	sanitize := basicPolicy()
	sanitize.Action = "sanitize"
	sanitize.BlockedWords = []string{"坏词"}
	cases := []struct {
		in  string
		pol conloomapi.ReviewPolicy
	}{
		{"你好", basicPolicy()},
		{"好的[CQ:at,qq=all]大家", basicPolicy()},
		{"key=abc123secretvalue", basicPolicy()},
		{"这里有坏词出现", block},
		{"这里有坏词出现", sanitize},
		{"一二三四五六七八九十十一", basicPolicy()},
		{"[CQ:face,id=1]", basicPolicy()},
	}
	for _, c := range cases {
		got := r.OutputProtected(c.in, c.pol)
		want := r.Output(c.in, c.pol)
		if got.Text != want.Text || got.Verdict != want.Verdict || !slices.Equal(got.Reasons, want.Reasons) {
			t.Errorf("OutputProtected(%q) = %+v, Output = %+v", c.in, got, want)
		}
	}
}

func TestOutput_DoesNotApplyProtectedLexicon(t *testing.T) {
	r := New(nil, Options{})
	for i, w := range lexiconEntries(t) {
		in := "前面" + w + "后面"
		got := r.Output(in, basicPolicy())
		if got.Verdict != VerdictAllow || got.Text != in || len(got.Reasons) != 0 {
			t.Errorf("entry #%d: Output = verdict %q reasons %v", i+1, got.Verdict, got.Reasons)
		}
	}
}
