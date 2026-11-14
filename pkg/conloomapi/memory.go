package conloomapi

import "time"

// MemoryState is what the bot remembers about a conversation. Enabled
// reports whether memory is in effect for the conversation; the stored data
// is shown either way. LastRunAt is nil before the first consolidation.
type MemoryState struct {
	ConversationID string              `json:"conversation_id"`
	Enabled        bool                `json:"enabled"`
	Summary        string              `json:"summary"`
	CursorSeq      int64               `json:"cursor_seq"`
	LastRunAt      *time.Time          `json:"last_run_at"`
	LastError      string              `json:"last_error"`
	Members        []MemberMemoryState `json:"members"`
	// SummaryMaxChars and NoteMaxChars are the summary and note limits in
	// effect: the policy's, or the conversation type's defaults.
	SummaryMaxChars int `json:"summary_max_chars"`
	NoteMaxChars    int `json:"note_max_chars"`
}

// MemberMemoryState is what the bot remembers about one member: UserID is the
// platform user ID. Affinity is nil and Tier empty when the member has no
// affinity yet.
type MemberMemoryState struct {
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Note      string    `json:"note"`
	Affinity  *int      `json:"affinity"`
	Tier      string    `json:"tier"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MemberMemoryPut changes a member's note, affinity or both; at least one of
// them is set. Reason is recorded with the change.
type MemberMemoryPut struct {
	Note     *string `json:"note,omitempty"`
	Affinity *int    `json:"affinity,omitempty"`
	Reason   string  `json:"reason"`
}
