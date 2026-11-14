package review

import (
	_ "embed"
	"encoding/base64"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ReasonProtectedContent is the review reason recorded when protected-mode
// output hits the built-in lexicon.
const ReasonProtectedContent = "protected_content"

// protectedSeparators are dropped before lexicon matching, besides white
// space and format characters such as zero-width spaces.
const protectedSeparators = "·・‧•._-*"

// protectedWordsB64 is the built-in lexicon of explicit sexual terms blocked
// in protected mode, one base64-encoded entry per line, so that the public
// source tree carries no plaintext explicit words.
//
//go:embed protected_words.b64
var protectedWordsB64 string

var protectedWords = mustDecodeLexicon(protectedWordsB64)

func mustDecodeLexicon(data string) []string {
	var words []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil {
			panic("review: invalid protected lexicon entry: " + err.Error())
		}
		if w := normalizeProtected(string(b)); w != "" {
			words = append(words, w)
		}
	}
	return words
}

// normalizeProtected applies NFKC, lower-cases, and drops white space, format
// characters and protectedSeparators, so that spaced, full-width, mixed-case
// or separator-split spellings match their lexicon entry.
func normalizeProtected(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsSpace(c) || unicode.Is(unicode.Cf, c) || strings.ContainsRune(protectedSeparators, c) {
			return -1
		}
		return c
	}, strings.ToLower(norm.NFKC.String(s)))
}

func containsProtected(text string) bool {
	n := normalizeProtected(text)
	for _, w := range protectedWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// OutputProtected is Output for protected mode: text that hits the built-in
// lexicon is blocked, regardless of every field of pol; other text goes
// through Output, whose result is checked against the lexicon again so that
// a stripped [CQ:...] code cannot split an entry.
func (r *Reviewer) OutputProtected(text string, pol conloomapi.ReviewPolicy) Result {
	if containsProtected(text) {
		return finish("", []string{ReasonProtectedContent}, true, true)
	}
	res := r.Output(text, pol)
	if res.Verdict != VerdictBlock && containsProtected(res.Text) {
		return finish("", append(res.Reasons, ReasonProtectedContent), true, true)
	}
	return res
}
