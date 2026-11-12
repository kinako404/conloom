// Package interest keeps per-user keyword interest profiles: a map from term
// to a decaying weight, built from messages a user addressed to the bot and
// used to cheaply pre-filter candidates for proactive replies.
package interest

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"unicode"
)

const (
	// MaxTerms is the number of highest-weight terms a profile keeps.
	MaxTerms = 50
	// Decay multiplies every existing weight before a message is added.
	Decay = 0.9
)

// Terms extracts the distinct terms of text in order of first appearance.
// A term is either a maximal run of ASCII letters (at least 2, lowercased)
// or a bigram of two adjacent Han characters. Stopwords from stopwords.go
// (including media placeholders such as "[表情]") are dropped, as is any
// bigram containing a single-character function word and any ASCII word made
// of one repeated letter ("hhh", "www").
func Terms(text string) []string {
	var terms []string
	seen := map[string]struct{}{}
	add := func(t string) {
		if _, dup := seen[t]; dup {
			return
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}

	var word strings.Builder
	flushWord := func() {
		if word.Len() >= 2 {
			w := word.String()
			if _, stop := stopWords[w]; !stop && !repeatedLetter(w) {
				add(w)
			}
		}
		word.Reset()
	}

	var prevHan rune
	for _, r := range text {
		if isASCIILetter(r) {
			word.WriteRune(unicode.ToLower(r))
		} else {
			flushWord()
		}

		if !unicode.Is(unicode.Han, r) {
			prevHan = 0
			continue
		}
		if prevHan != 0 {
			if b, ok := bigram(prevHan, r); ok {
				add(b)
			}
		}
		prevHan = r
	}
	flushWord()
	return terms
}

func bigram(a, b rune) (string, bool) {
	if _, stop := stopChars[a]; stop {
		return "", false
	}
	if _, stop := stopChars[b]; stop {
		return "", false
	}
	s := string([]rune{a, b})
	if _, stop := stopBigrams[s]; stop {
		return "", false
	}
	return s, true
}

func repeatedLetter(w string) bool {
	return strings.Count(w, w[:1]) == len(w)
}

func isASCIILetter(r rune) bool {
	return ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')
}

// Update returns a new profile: every existing weight multiplied by Decay,
// then +1 for each term of text, truncated to the MaxTerms highest-weight
// terms (ties broken as in Top). The input map is not modified.
func Update(profile map[string]float64, text string) map[string]float64 {
	next := make(map[string]float64, len(profile)+8)
	for t, w := range profile {
		next[t] = w * Decay
	}
	for _, t := range Terms(text) {
		next[t]++
	}
	if len(next) <= MaxTerms {
		return next
	}
	kept := make(map[string]float64, MaxTerms)
	for _, t := range Top(next, MaxTerms) {
		kept[t] = next[t]
	}
	return kept
}

// Top returns up to n terms ordered by weight descending, ties broken by
// ascending lexicographic order.
func Top(profile map[string]float64, n int) []string {
	if n <= 0 || len(profile) == 0 {
		return nil
	}
	terms := slices.SortedFunc(maps.Keys(profile), func(a, b string) int {
		if c := cmp.Compare(profile[b], profile[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	if len(terms) > n {
		terms = terms[:n]
	}
	return terms
}

// Match reports whether text shares at least K terms with the profile's top
// N terms, where level selects (N, K): "low" (20, 2), "mid" (30, 1),
// "high" (50, 1). matched lists the shared terms in text order and is nil
// whenever ok is false; any other level never matches.
func Match(profile map[string]float64, text, level string) (matched []string, ok bool) {
	var n, k int
	switch level {
	case "low":
		n, k = 20, 2
	case "mid":
		n, k = 30, 1
	case "high":
		n, k = 50, 1
	default:
		return nil, false
	}
	top := map[string]struct{}{}
	for _, t := range Top(profile, n) {
		top[t] = struct{}{}
	}
	if len(top) == 0 {
		return nil, false
	}
	for _, t := range Terms(text) {
		if _, hit := top[t]; hit {
			matched = append(matched, t)
		}
	}
	if len(matched) < k {
		return nil, false
	}
	return matched, true
}
