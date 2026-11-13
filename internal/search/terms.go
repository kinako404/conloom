package search

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	// MaxQueryTokens bounds the terms cut from a query.
	MaxQueryTokens = 8
	// MaxTerms bounds the terms of one search: keywords first, then the
	// query's tokens.
	MaxTerms = 16
	// wordPrefixRunes is the shortest word matched as a prefix at the end of
	// a term, so that "straw" finds "strawberry".
	wordPrefixRunes = 3
)

// Text is cut into runs of two kinds, after NFKC normalisation (full-width
// letters and digits become half-width, half-width katakana full-width) and
// lower-casing: CJK runs of Han, kana and Hangul, cut into every two adjacent
// characters, and word runs of other letters and digits (accented Latin
// letters included), kept whole. Everything else separates runs.

// Normalize is s as the keyword index sees it: NFKC, then lower case.
func Normalize(s string) string { return normalize(s) }

func normalize(s string) string { return strings.ToLower(norm.NFKC.String(s)) }

// QueryTokens splits query into at most MaxQueryTokens distinct search terms
// in order: every two adjacent characters of each CJK run and every word. A
// lone CJK character is a term only when the query has no other terms.
func QueryTokens(query string) []string {
	var tokens, singles []string
	add := func(list *[]string, tok string) {
		if !slices.Contains(*list, tok) {
			*list = append(*list, tok)
		}
	}
	scan(normalize(query), func(run []rune, cjk bool) {
		switch {
		case !cjk:
			add(&tokens, string(run))
		case len(run) == 1:
			add(&singles, string(run))
		default:
			for k := 0; k+1 < len(run); k++ {
				add(&tokens, string(run[k:k+2]))
			}
		}
	})
	if len(tokens) == 0 {
		tokens = singles
	}
	return tokens[:min(len(tokens), MaxQueryTokens)]
}

// Terms is the normalised keywords, each matched whole, followed by
// QueryTokens(query), without repeats and at most MaxTerms, so that a long
// query never crowds out the keywords.
func Terms(query string, keywords []string) []string {
	var terms []string
	for _, k := range keywords {
		if k = normalize(k); !slices.Contains(terms, k) {
			terms = append(terms, k)
		}
	}
	for _, tok := range QueryTokens(query) {
		if !slices.Contains(terms, tok) {
			terms = append(terms, tok)
		}
	}
	return terms[:min(len(terms), MaxTerms)]
}

// IndexTokens is what the keyword index stores for text, in order: for each
// CJK run every two adjacent characters followed by its last character
// alone, and every word.
func IndexTokens(text string) []string {
	var tokens []string
	scan(normalize(text), func(run []rune, cjk bool) {
		if !cjk {
			tokens = append(tokens, string(run))
			return
		}
		for k := 0; k+1 < len(run); k++ {
			tokens = append(tokens, string(run[k:k+2]))
		}
		tokens = append(tokens, string(run[len(run)-1]))
	})
	return tokens
}

// TermPhrase is how term is looked up in tokens made by IndexTokens: as the
// phrase of consecutive tokens returned, whose last one only needs to start
// the matching token when prefix is set.
//
// The phrase is IndexTokens(term) without the lone last character of the
// term's last run when that run is CJK and longer than one character: in the
// index that character, or a pair starting with it, always follows the pair
// before it, so leaving it out changes nothing and saves a prefix search. The
// last token is a prefix when the term ends in a lone CJK character or a word
// of at least wordPrefixRunes characters, since the text may go on there.
// Every piece of an indexed text that does not cut a word is thus found.
func TermPhrase(term string) (tokens []string, prefix bool) {
	var lastRun int
	scan(normalize(term), func(run []rune, cjk bool) {
		switch {
		case !cjk:
			tokens = append(tokens, string(run))
			prefix = len(run) >= wordPrefixRunes
		case len(run) == 1:
			tokens = append(tokens, string(run))
			prefix = true
		default:
			for k := 0; k+1 < len(run); k++ {
				tokens = append(tokens, string(run[k:k+2]))
			}
			tokens = append(tokens, string(run[len(run)-1]))
			prefix = false
		}
		lastRun = len(run)
		if !cjk {
			lastRun = 0
		}
	})
	if lastRun > 1 {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens, prefix
}

// ConversationMark is the token that marks a conversation's messages in the
// keyword index: "c" and the first 16 hex digits of the SHA-256 of convKey.
func ConversationMark(convKey string) string {
	sum := sha256.Sum256([]byte(convKey))
	return "c" + hex.EncodeToString(sum[:8])
}

// IndexText is the indexed text of a message: the IndexTokens of its text
// and of its sender's display name, separated by spaces.
func IndexText(displayName, text string) string {
	return strings.Join(append(IndexTokens(text), IndexTokens(displayName)...), " ")
}

// scan calls fn with each CJK run (cjk set) and each word run of s, in order.
func scan(s string, fn func(run []rune, cjk bool)) {
	runes := []rune(s)
	for i := 0; i < len(runes); {
		var in func(rune) bool
		switch {
		case isCJK(runes[i]):
			in = isCJK
		case isWord(runes[i]):
			in = isWord
		default:
			i++
			continue
		}
		j := i
		for j < len(runes) && in(runes[j]) {
			j++
		}
		fn(runes[i:j], isCJK(runes[i]))
		i = j
	}
}

// isCJK reports whether r is Han, kana (with the prolonged sound mark) or
// Hangul.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || r == 'ー'
}

// isWord reports whether r is a letter, digit or mark outside CJK.
func isWord(r rune) bool {
	return !isCJK(r) && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
}
