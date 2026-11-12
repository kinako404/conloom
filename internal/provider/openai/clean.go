package openai

import (
	"regexp"
	"strings"
)

var (
	thinkBlock = regexp.MustCompile(`(?is)<think(?:ing)?>.*?</think(?:ing)?>`)
	thinkOpen  = regexp.MustCompile(`(?i)<think(?:ing)?>`)
	thinkClose = regexp.MustCompile(`(?i)</think(?:ing)?>`)
)

// CleanOutput strips model reasoning and trims surrounding whitespace. Tags
// <think>/</think> and <thinking>/</thinking> match case-insensitively. Every
// complete block is removed, an unclosed opening tag removes everything after
// it, and a remaining bare closing tag (the opening tag was part of the
// prompt template) removes everything up to and including its last
// occurrence.
func CleanOutput(s string) string {
	s = thinkBlock.ReplaceAllString(s, "")
	if loc := thinkOpen.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	if all := thinkClose.FindAllStringIndex(s, -1); len(all) > 0 {
		s = s[all[len(all)-1][1]:]
	}
	return strings.TrimSpace(s)
}
