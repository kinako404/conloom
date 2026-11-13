package search

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
)

const (
	// MaxQueryTokens bounds the terms cut from a query.
	MaxQueryTokens = 8
	// MaxTerms bounds the terms of one search: keywords first, then the
	// query's tokens.
	MaxTerms = 16
	// asciiPrefixRunes is the shortest ASCII word matched as a prefix at the
	// end of a term, so that "straw" finds "strawberry".
	asciiPrefixRunes = 3
)

// QueryTokens splits query into at most MaxQueryTokens distinct search terms
// in order: every two adjacent characters of each run of Han characters and
// every lower-cased run of ASCII letters or digits. A lone Han character is a
// term only when the query has no other terms.
func QueryTokens(query string) []string {
	var tokens, singles []string
	add := func(list *[]string, tok string) {
		if !slices.Contains(*list, tok) {
			*list = append(*list, tok)
		}
	}
	scan(query, func(run []rune, han bool) {
		switch {
		case !han:
			add(&tokens, strings.ToLower(string(run)))
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

// Terms is the lower-cased keywords, each matched whole, followed by
// QueryTokens(query), without repeats and at most MaxTerms, so that a long
// query never crowds out the keywords.
func Terms(query string, keywords []string) []string {
	var terms []string
	for _, k := range keywords {
		if k = strings.ToLower(k); !slices.Contains(terms, k) {
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
// run of Han characters every two adjacent characters followed by its last
// character alone, and every lower-cased run of ASCII letters or digits.
// Other characters separate tokens. Together with TermPhrase this finds a
// term wherever it occurs in text, as long as an ASCII word in it is not cut.
func IndexTokens(text string) []string {
	var tokens []string
	scan(text, func(run []rune, han bool) {
		if !han {
			tokens = append(tokens, strings.ToLower(string(run)))
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
// the matching token when prefix is set. That is IndexTokens(term) without
// the lone last character of Han runs longer than one (their bigrams cover
// it), and prefix when the term ends in a lone Han character or an ASCII word
// of at least three characters.
func TermPhrase(term string) (tokens []string, prefix bool) {
	scan(term, func(run []rune, han bool) {
		switch {
		case !han:
			tokens = append(tokens, strings.ToLower(string(run)))
			prefix = len(run) >= asciiPrefixRunes
		case len(run) == 1:
			tokens = append(tokens, string(run))
			prefix = true
		default:
			for k := 0; k+1 < len(run); k++ {
				tokens = append(tokens, string(run[k:k+2]))
			}
			prefix = false
		}
	})
	return tokens, prefix
}

// ConversationMark is the token that marks a conversation's messages in the
// keyword index: "c" and the first 16 hex digits of the SHA-256 of convKey.
func ConversationMark(convKey string) string {
	sum := sha256.Sum256([]byte(convKey))
	return "c" + hex.EncodeToString(sum[:8])
}

// IndexText is the indexed text of a message: its conversation's mark, then
// the IndexTokens of its text and of its sender's display name, separated by
// spaces.
func IndexText(convKey, displayName, text string) string {
	parts := append([]string{ConversationMark(convKey)}, IndexTokens(text)...)
	return strings.Join(append(parts, IndexTokens(displayName)...), " ")
}

// scan calls fn with each run of Han characters (han set) and each run of
// ASCII letters or digits in s, in order.
func scan(s string, fn func(run []rune, han bool)) {
	runes := []rune(s)
	for i := 0; i < len(runes); {
		var in func(rune) bool
		switch {
		case isHan(runes[i]):
			in = isHan
		case isASCIIAlnum(runes[i]):
			in = isASCIIAlnum
		default:
			i++
			continue
		}
		j := i
		for j < len(runes) && in(runes[j]) {
			j++
		}
		fn(runes[i:j], isHan(runes[i]))
		i = j
	}
}

func isHan(r rune) bool { return unicode.Is(unicode.Han, r) }

func isASCIIAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
