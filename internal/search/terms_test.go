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
		{"", nil},
		{"？！", nil},
	} {
		if got := IndexTokens(c.text); !slices.Equal(got, c.want) {
			t.Errorf("IndexTokens(%q) = %q, want %q", c.text, got, c.want)
		}
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
		{"？", nil, false},
	} {
		got, prefix := TermPhrase(c.term)
		if !slices.Equal(got, c.want) || prefix != c.prefix {
			t.Errorf("TermPhrase(%q) = %q, %v; want %q, %v", c.term, got, prefix, c.want, c.prefix)
		}
	}
}

// TestTermPhraseMatchesSubstrings checks that a term's phrase occurs in the
// index tokens of a text exactly when the term occurs in it, for Han text.
func TestTermPhraseMatchesSubstrings(t *testing.T) {
	texts := []string{"我们出去玩吧", "出去 玩", "周末去玩", "小猫咪", "家猫", "猫", "a猫咪", "小猫a", "我在学Go语言", "草莓蛋糕"}
	terms := []string{"出去玩", "去玩", "猫", "猫咪", "a猫", "猫a", "go语言", "草莓", "蛋糕"}
	for _, text := range texts {
		tokens := IndexTokens(text)
		for _, term := range terms {
			phrase, prefix := TermPhrase(term)
			found := false
			for i := 0; i+len(phrase) <= len(tokens) && !found; i++ {
				found = true
				for j, p := range phrase {
					tok := tokens[i+j]
					if tok != p && !(prefix && j == len(phrase)-1 && strings.HasPrefix(tok, p)) {
						found = false
						break
					}
				}
			}
			if want := strings.Contains(strings.ToLower(text), strings.ToLower(term)); found != want {
				t.Errorf("%q in %q: phrase %q (prefix %v) found = %v, want %v", term, text, phrase, prefix, found, want)
			}
		}
	}
}

func TestConversationMark(t *testing.T) {
	a, b := ConversationMark("bot:qq:group:group_10001"), ConversationMark("bot:qq:group:group_10002")
	if !regexp.MustCompile(`^c[0-9a-f]{16}$`).MatchString(a) || a == b || a != ConversationMark("bot:qq:group:group_10001") {
		t.Fatalf("marks = %q, %q", a, b)
	}
	if got, want := IndexText("bot:qq:group:group_10001", "小明", "草莓！"), a+" 草莓 莓 小明 明"; got != want {
		t.Fatalf("IndexText = %q, want %q", got, want)
	}
}
