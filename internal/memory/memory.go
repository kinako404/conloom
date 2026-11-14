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

// ReasonAffinityCommand is recorded on revisions written by an admin's
// /好感度 设|+n|-n, which overrides the affinity rules.
const ReasonAffinityCommand = "affinity_command"

// Default storage limits, in runes, of a private conversation's summary and
// note, where the bot talks to one person and can remember more about them;
// groups default to SummaryMaxRunes and NoteMaxRunes. A conversation's
// memory policy may set its own (see SummaryLimit and NoteLimit).
const (
	PrivateSummaryMaxRunes = 3000
	PrivateNoteMaxRunes    = 1000
)

// SummaryLimit is the summary length limit, in runes, of a conversation of
// convType under memory policy pol: pol's summary_max_chars, or the default
// of convType when it is 0.
func SummaryLimit(pol conloomapi.MemoryPolicy, convType string) int {
	switch {
	case pol.SummaryMaxChars > 0:
		return pol.SummaryMaxChars
	case convType == conloomapi.ConvPrivate:
		return PrivateSummaryMaxRunes
	}
	return SummaryMaxRunes
}

// NoteLimit is the note length limit, in runes, of a conversation of
// convType under memory policy pol: pol's note_max_chars, or the default of
// convType when it is 0.
func NoteLimit(pol conloomapi.MemoryPolicy, convType string) int {
	switch {
	case pol.NoteMaxChars > 0:
		return pol.NoteMaxChars
	case convType == conloomapi.ConvPrivate:
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
