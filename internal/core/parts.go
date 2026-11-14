package core

import (
	"strings"
	"time"
	"unicode/utf8"
)

// partSeparator, alone on a line, separates the parts of a reply.
const partSeparator = "|||"

const (
	partDelayBase    = 800 * time.Millisecond
	partDelayPerRune = 60 * time.Millisecond
	partDelayMin     = time.Second
	partDelayMax     = 4 * time.Second
)

// splitParts splits a reply into at most max parts at lines that hold only
// partSeparator (and blanks). Parts are trimmed and empty ones dropped; those
// beyond max are joined, by newlines, into the last one. With max <= 1 the
// text is its only part, unchanged.
func splitParts(text string, max int) []string {
	if max <= 1 {
		return []string{text}
	}
	var parts []string
	var cur []string
	flush := func() {
		if p := strings.TrimSpace(strings.Join(cur, "\n")); p != "" {
			parts = append(parts, p)
		}
		cur = cur[:0]
	}
	for line := range strings.Lines(text) {
		if strings.TrimSpace(line) == partSeparator {
			flush()
			continue
		}
		cur = append(cur, strings.TrimRight(line, "\r\n"))
	}
	flush()
	if len(parts) > max {
		parts = append(parts[:max-1], strings.Join(parts[max-1:], "\n"))
	}
	return parts
}

// partDelay is how long after the part before it a later part of a reply is
// sent: the time to type it, so longer parts take longer.
func partDelay(part string) time.Duration {
	d := partDelayBase + partDelayPerRune*time.Duration(utf8.RuneCountInString(part))
	return min(max(d, partDelayMin), partDelayMax)
}
