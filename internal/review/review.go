// Package review implements the deterministic basic review pipeline applied
// to inbound user text and outbound model text. The basic rules cannot be
// turned off by policy.
package review

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	VerdictAllow    = "allow"
	VerdictSanitize = "sanitize"
	VerdictBlock    = "block"
)

const (
	defaultMaxInputRunes  = 4000
	defaultMaxOutputRunes = 1500

	truncationMarker = "…（内容过长，已截断）"
	redacted         = "[已隐藏]"

	reasonControlChars = "control_chars"
	reasonTruncated    = "truncated"
	reasonCQCode       = "cq_code"
	reasonSecret       = "secret"
	reasonBlockedWord  = "blocked_word"
	reasonEmpty        = "empty"
)

var (
	cqCodePattern  = regexp.MustCompile(`\[CQ:[^\]]*\]`)
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]{16,}`),
		regexp.MustCompile(`[0-9A-Fa-f]{32,}`),
	}
)

type Result struct {
	Text    string
	Verdict string
	Reasons []string
}

type Options struct {
	MaxInputRunes  int
	MaxOutputRunes int
}

type Reviewer struct {
	secrets []string
	opts    Options
}

// New returns a Reviewer that redacts the given secret values from output.
// Non-positive limits in opts fall back to the defaults (4000 input runes,
// 1500 output runes).
func New(secrets []string, opts Options) *Reviewer {
	var s []string
	for _, v := range secrets {
		if v != "" {
			s = append(s, v)
		}
	}
	slices.SortFunc(s, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	if opts.MaxInputRunes <= 0 {
		opts.MaxInputRunes = defaultMaxInputRunes
	}
	if opts.MaxOutputRunes <= 0 {
		opts.MaxOutputRunes = defaultMaxOutputRunes
	}
	return &Reviewer{secrets: s, opts: opts}
}

// Input strips control characters other than \n and \t and truncates the text
// to the input rune limit, appending a truncation marker. It ignores
// pol.ReviewInput because the basic rules are always on.
func (r *Reviewer) Input(text string, _ conloomapi.ReviewPolicy) Result {
	var reasons []string
	cleaned := strings.Map(func(c rune) rune {
		if c != '\n' && c != '\t' && unicode.IsControl(c) {
			return -1
		}
		return c
	}, text)
	if cleaned != text {
		reasons = append(reasons, reasonControlChars)
	}
	if t, ok := truncateRunes(cleaned, r.opts.MaxInputRunes); ok {
		cleaned = t + truncationMarker
		reasons = append(reasons, reasonTruncated)
	}
	return finish(cleaned, reasons, cleaned != text, false)
}

// Output runs the outbound pipeline: strip [CQ:...] codes, redact secrets,
// apply blocked words per pol.Action, truncate to the output rune limit, and
// block blank results. It ignores pol.ReviewOutput because the basic rules are
// always on.
func (r *Reviewer) Output(text string, pol conloomapi.ReviewPolicy) Result {
	var reasons []string

	out := text
	for {
		next := cqCodePattern.ReplaceAllString(out, "")
		if next == out {
			break
		}
		out = next
	}
	if out != text {
		reasons = append(reasons, reasonCQCode)
	}

	before := out
	for _, s := range r.secrets {
		out = strings.ReplaceAll(out, s, redacted)
	}
	for _, p := range secretPatterns {
		out = p.ReplaceAllString(out, redacted)
	}
	if out != before {
		reasons = append(reasons, reasonSecret)
	}

	matched := false
	for _, w := range pol.BlockedWords {
		if w == "" || !strings.Contains(out, w) {
			continue
		}
		matched = true
		if pol.Action == VerdictSanitize {
			out = strings.ReplaceAll(out, w, strings.Repeat("*", utf8.RuneCountInString(w)))
		}
	}
	if matched {
		reasons = append(reasons, reasonBlockedWord)
		if pol.Action == VerdictBlock {
			return finish("", reasons, true, true)
		}
	}

	if t, ok := truncateRunes(out, r.opts.MaxOutputRunes); ok {
		out = t
		reasons = append(reasons, reasonTruncated)
	}

	if strings.TrimSpace(out) == "" {
		return finish("", append(reasons, reasonEmpty), true, true)
	}
	return finish(out, reasons, out != text, false)
}

// LevelImplemented reports whether the review level has its own behavior in
// this build; other levels behave like basic.
func LevelImplemented(level string) bool {
	return level == "basic"
}

func finish(text string, reasons []string, modified, block bool) Result {
	res := Result{Text: text, Verdict: VerdictAllow, Reasons: reasons}
	switch {
	case block:
		res.Verdict = VerdictBlock
	case modified:
		res.Verdict = VerdictSanitize
	}
	return res
}

func truncateRunes(s string, limit int) (string, bool) {
	if utf8.RuneCountInString(s) <= limit {
		return s, false
	}
	i := 0
	for n := 0; n < limit; n++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i], true
}
