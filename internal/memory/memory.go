// Package memory keeps what the bot remembers about each group: a rolling
// summary, a note per member and an affinity score, updated by background
// consolidation and by fixed affinity rules.
package memory

import (
	"context"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Result describes one consolidation run. Skipped is empty when the run did
// its work, otherwise one of "disabled", "backoff", "no_messages",
// "long_unavailable", "busy" or "forgotten" (a private conversation's member
// was forgotten during the run).
type Result struct {
	Batches, Messages, MembersUpdated int
	Skipped                           string
}

// Runner consolidates one conversation's new messages; manual is true when an
// admin asked for the run.
type Runner interface {
	Run(ctx context.Context, convKey string, manual bool) (Result, error)
}

// Reasons recorded on revisions written by the affinity rules.
const (
	ReasonFirstInteraction = "first_interaction"
	ReasonDaily            = "daily"
	ReasonDecay            = "decay"
)

// Storage limits of private conversations, where the bot talks to one person
// and can remember more about them; groups use SummaryMaxRunes and
// NoteMaxRunes.
const (
	PrivateSummaryMaxRunes = 1200
	PrivateNoteMaxRunes    = 500
)

// SummaryLimit is the summary length limit, in runes, of conversations of
// convType.
func SummaryLimit(convType string) int {
	if convType == conloomapi.ConvPrivate {
		return PrivateSummaryMaxRunes
	}
	return SummaryMaxRunes
}

// NoteLimit is the note length limit, in runes, of conversations of convType.
func NoteLimit(convType string) int {
	if convType == conloomapi.ConvPrivate {
		return PrivateNoteMaxRunes
	}
	return NoteMaxRunes
}

// convTypeOf is the type part of a "bot:platform:type:id" conversation key.
func convTypeOf(convKey string) string {
	if parts := strings.SplitN(convKey, ":", 4); len(parts) == 4 {
		return parts[2]
	}
	return ""
}
