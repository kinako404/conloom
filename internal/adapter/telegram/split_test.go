package telegram

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSplitText(t *testing.T) {
	a, b := strings.Repeat("甲", 3000), strings.Repeat("乙", 3000)
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"short", "你好", []string{"你好"}},
		{"exactly the limit", strings.Repeat("好", 4096), []string{strings.Repeat("好", 4096)}},
		{"no break", strings.Repeat("好", 5000), []string{strings.Repeat("好", 4096), strings.Repeat("好", 904)}},
		{"paragraph", a + "\n\n" + b, []string{a, b}},
		{"line", a + "\n" + b, []string{a, b}},
		{"prefers the paragraph", a + "\n\n" + strings.Repeat("丙", 500) + "\n" + b, []string{a, strings.Repeat("丙", 500) + "\n" + b}},
		{"early newline is ignored", "开头\n" + strings.Repeat("好", 5000),
			[]string{"开头\n" + strings.Repeat("好", 4093), strings.Repeat("好", 907)}},
		{"surrogate pairs", strings.Repeat("😀", 3000), []string{strings.Repeat("😀", 2048), strings.Repeat("😀", 952)}},
	}
	for _, c := range cases {
		got := splitText(c.in, maxMessageLen)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %d parts with lengths %v, want %d parts with lengths %v",
				c.name, len(got), utf16Lens(got), len(c.want), utf16Lens(c.want))
		}
		for i, p := range got {
			if n := len(utf16.Encode([]rune(p))); n > maxMessageLen {
				t.Errorf("%s: part %d has %d UTF-16 units, over the limit", c.name, i, n)
			}
		}
	}
}

func utf16Lens(parts []string) []int {
	var out []int
	for _, p := range parts {
		out = append(out, len(utf16.Encode([]rune(p))))
	}
	return out
}
