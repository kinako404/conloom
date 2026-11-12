package openai

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// CleanOutput removes every <think>…</think> block (an unclosed <think>
// removes everything after it) and trims surrounding whitespace.
func CleanOutput(s string) string {
	for {
		i := strings.Index(s, thinkOpen)
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], thinkClose)
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + s[i+j+len(thinkClose):]
	}
	return strings.TrimSpace(s)
}
