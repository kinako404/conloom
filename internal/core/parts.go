package core

import (
	"strings"
	"sync"
	"time"
	"unicode"
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

// ellipsisTail holds the characters an ellipsis run takes along: more
// ellipsis dots, closing quotes and brackets, and punctuation that ends the
// pause, so no part starts with them.
const ellipsisTail = "….。”’」』）)】》\"'？！?!，,、；;：:～~"

// splitParts splits a reply into at most max parts. With a fenced code block
// in the text, only lines that hold only partSeparator (and blanks) split it.
// Otherwise those lines, every line break, and every ellipsis ("…", or three
// or more "." or "。") with text both before and after it on its line split
// it; the ellipsis and the punctuation right after it stay with the text
// before. Parts are trimmed and empty ones dropped; those beyond max are
// joined into the last one by what separated them: a newline for lines, the
// spaces after an ellipsis. With max <= 1 the text is its only part,
// unchanged.
func splitParts(text string, max int) []string {
	if max <= 1 {
		return []string{text}
	}
	if strings.Contains(text, "```") {
		return splitAtSeparators(text, max)
	}
	var parts, seps []string
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == partSeparator {
			continue
		}
		for i, seg := range splitAtEllipses(line) {
			p := strings.TrimSpace(seg)
			if p == "" {
				continue
			}
			sep := "\n"
			if i > 0 {
				sep = seg[:len(seg)-len(strings.TrimLeftFunc(seg, unicode.IsSpace))]
			}
			parts = append(parts, p)
			seps = append(seps, sep)
		}
	}
	if len(parts) > max {
		var last strings.Builder
		last.WriteString(parts[max-1])
		for i := max; i < len(parts); i++ {
			last.WriteString(seps[i])
			last.WriteString(parts[i])
		}
		parts = append(parts[:max-1], last.String())
	}
	return parts
}

// splitAtSeparators splits text into at most max parts at lines that hold
// only partSeparator (and blanks), as splitParts does for text with code.
func splitAtSeparators(text string, max int) []string {
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

// splitAtEllipses splits a line right after each ellipsis and the
// ellipsisTail characters following it, when there is text both before it
// (since the previous split) and after it. The segments are not trimmed.
func splitAtEllipses(line string) []string {
	var segs []string
	start := 0
	for i := 0; i < len(line); {
		if !ellipsisAt(line[i:]) {
			_, w := utf8.DecodeRuneInString(line[i:])
			i += w
			continue
		}
		end := i
		for end < len(line) {
			r, w := utf8.DecodeRuneInString(line[end:])
			if !strings.ContainsRune(ellipsisTail, r) {
				break
			}
			end += w
		}
		if hasText(line[start:i]) && hasText(line[end:]) {
			segs = append(segs, line[start:end])
			start = end
		}
		i = end
	}
	return append(segs, line[start:])
}

// ellipsisAt reports whether s starts with an ellipsis: "…", "..." or "。。。".
func ellipsisAt(s string) bool {
	return strings.HasPrefix(s, "…") || strings.HasPrefix(s, "...") || strings.HasPrefix(s, "。。。")
}

// hasText reports whether s holds anything but spaces and punctuation.
func hasText(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return !unicode.IsSpace(r) && !unicode.IsPunct(r) && r != '~' && r != '～'
	})
}

// partDelay is how long after the part before it a later part of a reply is
// sent: the time to type it, so longer parts take longer.
func partDelay(part string) time.Duration {
	d := partDelayBase + partDelayPerRune*time.Duration(utf8.RuneCountInString(part))
	return min(max(d, partDelayMin), partDelayMax)
}

// partWakes runs a function once each later part of a stored reply is due, so
// adapters waiting in a long poll get it then rather than at their next
// recheck.
type partWakes struct {
	mu     sync.Mutex
	closed bool
	timers map[*time.Timer]struct{}
}

// after runs fn after d, unless stop is called first.
func (w *partWakes) after(d time.Duration, fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.timers == nil {
		w.timers = map[*time.Timer]struct{}{}
	}
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		w.mu.Lock()
		_, live := w.timers[t]
		delete(w.timers, t)
		w.mu.Unlock()
		if live {
			fn()
		}
	})
	w.timers[t] = struct{}{}
}

// stop cancels every pending function for good.
func (w *partWakes) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for t := range w.timers {
		t.Stop()
	}
	w.timers = nil
}
