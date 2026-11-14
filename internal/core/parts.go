package core

import (
	"strings"
	"sync"
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
