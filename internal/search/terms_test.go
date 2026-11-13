package search

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestQueryTokens(t *testing.T) {
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"我喜欢草莓", []string{"我喜", "喜欢", "欢草", "草莓"}},
		{"喜欢 Go 1.26 和 SQLite", []string{"喜欢", "go", "1", "26", "sqlite"}},
		{"猫", []string{"猫"}},
		{"一二三四五六七八九十", []string{"一二", "二三", "三四", "四五", "五六", "六七", "七八", "八九"}},
		{"草莓草莓 GO go", []string{"草莓", "莓草", "go"}},
		{"？？！", nil},
	} {
		if got := QueryTokens(c.query); !slices.Equal(got, c.want) {
			t.Errorf("QueryTokens(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

func TestTerms(t *testing.T) {
	for _, c := range []struct {
		query    string
		keywords []string
		want     []string
	}{
		{"旅游", nil, []string{"旅游"}},
		{"旅游", []string{"出去玩", "旅游", "Go 语言", "旅行"}, []string{"出去玩", "旅游", "go 语言", "旅行"}},
		{"？", []string{"出去玩"}, []string{"出去玩"}},
		{"一二三四五六七八九", []string{"出去玩"}, []string{"出去玩", "一二", "二三", "三四", "四五", "五六", "六七", "七八", "八九"}},
		{"一二三四五六七八九十", []string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛"},
			[]string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "一二", "二三", "三四", "四五", "五六", "六七", "七八", "八九"}},
		{"一二三", []string{"二三", "x"}, []string{"二三", "x", "一二"}},
	} {
		if got := Terms(c.query, c.keywords); !slices.Equal(got, c.want) {
			t.Errorf("Terms(%q, %q) = %q, want %q", c.query, c.keywords, got, c.want)
		}
	}
}

func TestIndexTokens(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"我家猫很可爱", []string{"我家", "家猫", "猫很", "很可", "可爱", "爱"}},
		{"猫", []string{"猫"}},
		{"Go 1.26 和 SQLite！", []string{"go", "1", "26", "和", "sqlite"}},
		{"草莓草莓", []string{"草莓", "莓草", "草莓", "莓"}},
		{"a猫b", []string{"a", "猫", "b"}},
		{"ＱＱ号是２０２６", []string{"qq", "号是", "是", "2026"}},
		{"東京に行きたい", []string{"東京", "京に", "に行", "行き", "きた", "たい", "い"}},
		{"ラーメン", []string{"ラー", "ーメ", "メン", "ン"}},
		{"한국어 공부", []string{"한국", "국어", "어", "공부", "부"}},
		{"Café au lait", []string{"café", "au", "lait"}},
		{"ｶﾞｷﾞ", []string{"ガギ", "ギ"}},
		{"", nil},
		{"？！", nil},
	} {
		if got := IndexTokens(c.text); !slices.Equal(got, c.want) {
			t.Errorf("IndexTokens(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestQueryTokensNormalise(t *testing.T) {
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"ＱＱ号", []string{"qq"}},
		{"ＱＱ", []string{"qq"}},
		{"ラーメン", []string{"ラー", "ーメ", "メン"}},
		{"CAFÉ", []string{"café"}},
	} {
		if got := QueryTokens(c.query); !slices.Equal(got, c.want) {
			t.Errorf("QueryTokens(%q) = %q, want %q", c.query, got, c.want)
		}
	}
	if got := Terms("x", []string{"ＱＱ"}); !slices.Equal(got, []string{"qq", "x"}) {
		t.Errorf("Terms with a full-width keyword = %q", got)
	}
}

func TestTermPhrase(t *testing.T) {
	for _, c := range []struct {
		term   string
		want   []string
		prefix bool
	}{
		{"草莓", []string{"草莓"}, false},
		{"出去玩", []string{"出去", "去玩"}, false},
		{"猫", []string{"猫"}, true},
		{"go 语言", []string{"go", "语言"}, false},
		{"a猫", []string{"a", "猫"}, true},
		{"猫a", []string{"猫", "a"}, false},
		{"strawberry", []string{"strawberry"}, true},
		{"Straw", []string{"straw"}, true},
		{"go", []string{"go"}, false},
		{"手机iPhone", []string{"手机", "机", "iphone"}, true},
		{"小米14", []string{"小米", "米", "14"}, false},
		{"出去 玩", []string{"出去", "去", "玩"}, true},
		{"？", nil, false},
	} {
		got, prefix := TermPhrase(c.term)
		if !slices.Equal(got, c.want) || prefix != c.prefix {
			t.Errorf("TermPhrase(%q) = %q, %v; want %q, %v", c.term, got, prefix, c.want, c.prefix)
		}
	}
}

// phraseIn reports whether term's TermPhrase occurs among tokens.
func phraseIn(term string, tokens []string) bool {
	phrase, prefix := TermPhrase(term)
	if len(phrase) == 0 {
		return false
	}
	for i := 0; i+len(phrase) <= len(tokens); i++ {
		ok := true
		for j, p := range phrase {
			tok := tokens[i+j]
			if tok != p && !(prefix && j == len(phrase)-1 && strings.HasPrefix(tok, p)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// TestTermPhraseFindsEverySubstring checks the index and the query side
// agree: every piece of an indexed text that does not cut a word is found by
// its own TermPhrase, across scripts, mixed text and several runs.
func TestTermPhraseFindsEverySubstring(t *testing.T) {
	texts := []string{
		"我的手机iPhone坏了", "新出的小米14发布了", "我们出去 玩吧", "周末出去玩，好不好", "a猫b猫c",
		"東京に行きたいです", "한국어 공부 시작", "Café au lait 很好喝", "ＱＱ号是２０２６年注册的", "Go 1.26 和 SQLite！",
	}
	for _, text := range texts {
		tokens := IndexTokens(text)
		runes := []rune(normalize(text))
		cut := func(i int) bool { return i > 0 && i < len(runes) && isWord(runes[i-1]) && isWord(runes[i]) }
		for i := 0; i <= len(runes); i++ {
			for j := i + 1; j <= len(runes); j++ {
				if cut(i) || cut(j) {
					continue
				}
				if term := string(runes[i:j]); len(IndexTokens(term)) > 0 && !phraseIn(term, tokens) {
					p, prefix := TermPhrase(term)
					t.Errorf("%q not found in %q: phrase %q (prefix %v), tokens %q", term, text, p, prefix, tokens)
				}
			}
		}
	}
}

func TestTermPhraseMissesWhatIsAbsent(t *testing.T) {
	for _, c := range []struct{ term, text string }{
		{"出去玩", "出去 玩"},
		{"出去 玩", "我们出去玩吧"},
		{"去玩", "周末出去，玩吧"},
		{"猫a", "a猫咪"},
		{"手机iPhone", "手机 华为 iPhone"},
		{"小米14", "小米 13 和 14"},
		{"phone", "iPhone"},
		{"東京", "京都"},
	} {
		if phraseIn(c.term, IndexTokens(c.text)) {
			t.Errorf("%q found in %q", c.term, c.text)
		}
	}
}

func TestConversationMark(t *testing.T) {
	a, b := ConversationMark("bot:qq:group:group_10001"), ConversationMark("bot:qq:group:group_10002")
	if !regexp.MustCompile(`^c[0-9a-f]{16}$`).MatchString(a) || a == b || a != ConversationMark("bot:qq:group:group_10001") {
		t.Fatalf("marks = %q, %q", a, b)
	}
	if got, want := IndexText("小明", "草莓！"), "草莓 莓 小明 明"; got != want {
		t.Fatalf("IndexText = %q, want %q", got, want)
	}
}
