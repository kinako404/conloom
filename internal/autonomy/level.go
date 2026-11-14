// Package autonomy holds the rules of private-chat autonomy that need no
// storage: how often the bot may start a chat, when it stays quiet, when it
// next reaches out, how the hidden bars decay and how the hidden state is
// shown to the reply model.
package autonomy

import (
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// MaxUnanswered is how many messages the bot may start (the initiate and its
// follow-up together) since the user's last message.
const MaxUnanswered = 2

// DailyCap returns how many messages the bot may start per local day at
// level; 0 for off and for an unknown or empty level (callers resolve an
// empty level with PrivateProactivePolicy.EffectiveLevel).
func DailyCap(level string) int {
	switch level {
	case conloomapi.PrivateLevelLow:
		return 1
	case conloomapi.PrivateLevelMid:
		return 2
	case conloomapi.PrivateLevelHigh:
		return 4
	}
	return 0
}

// MinGap returns how long after the last message of the chat, from either
// side, the bot waits before starting one at level; 0 for off and unknown.
func MinGap(level string) time.Duration {
	switch level {
	case conloomapi.PrivateLevelLow:
		return 8 * time.Hour
	case conloomapi.PrivateLevelMid:
		return 4 * time.Hour
	case conloomapi.PrivateLevelHigh:
		return 2 * time.Hour
	}
	return 0
}
