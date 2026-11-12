// Package contextbuilder assembles the chat-completion message list for a
// reply from stored conversation history within a token budget.
package contextbuilder

import "math"

// MessageOverhead is the estimated per-message token cost on top of content.
const MessageOverhead = 4

// Estimate approximates the token count of text: each CJK/kana/hangul rune
// (r >= 0x2E80) counts 1, every 4 other runes count 1 (rounded up), and the
// total is multiplied by factor (<= 0 means 1) and rounded up, ignoring
// floating-point noise below 1e-9 (100 * 1.1 is 110, not 111).
func Estimate(text string, factor float64) int {
	wide, other := 0, 0
	for _, r := range text {
		if r >= 0x2E80 {
			wide++
		} else {
			other++
		}
	}
	raw := wide + (other+3)/4
	if factor <= 0 {
		factor = 1
	}
	return int(math.Ceil(float64(raw)*factor - 1e-9))
}
