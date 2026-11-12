// Package memory keeps what the bot remembers about each group: a rolling
// summary, a note per member and an affinity score, updated by background
// consolidation and by fixed affinity rules.
package memory

import "context"

// Result describes one consolidation run. Skipped is empty when the run did
// its work, otherwise one of "disabled", "backoff", "no_messages",
// "long_unavailable" or "busy".
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
