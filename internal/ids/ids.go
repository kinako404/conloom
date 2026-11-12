// Package ids generates prefixed, roughly time-ordered unique identifiers.
package ids

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns prefix + "_" + 26 Crockford base32 characters encoding a 48-bit
// Unix millisecond timestamp followed by 80 random bits.
func New(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	_, _ = rand.Read(b[6:])
	return prefix + "_" + encode(b)
}

// encode renders the 128-bit value as 26 base32 digits, left-padded with two zero bits.
func encode(b [16]byte) string {
	bit := func(k int) byte {
		k -= 2
		if k < 0 {
			return 0
		}
		return (b[k/8] >> (7 - k%8)) & 1
	}
	var out [26]byte
	for j := range out {
		var v byte
		for k := j * 5; k < j*5+5; k++ {
			v = v<<1 | bit(k)
		}
		out[j] = crockford[v]
	}
	return string(out[:])
}
