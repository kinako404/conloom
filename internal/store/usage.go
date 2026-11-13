package store

import "time"

const (
	PurposeChat       = "chat"
	PurposeMemory     = "memory"
	PurposeEmbedIndex = "embed_index"
	PurposeEmbedQuery = "embed_query"
)

const (
	OutcomeOK       = "ok"
	OutcomeError    = "error"
	OutcomeTimeout  = "timeout"
	OutcomeCanceled = "canceled"
)

// ModelCall records one model or embedding call. Purpose is one of the
// Purpose* constants and Outcome one of the Outcome* constants.
type ModelCall struct {
	ID                                              int64
	At                                              time.Time
	BotInstanceID, ConversationKey, TaskID          string
	Purpose, Role, Model                            string
	PromptTokens, CompletionTokens, ReasoningTokens int
	EstimatedTokens                                 int
	LatencyMS                                       int64
	Outcome, FinishReason                           string
}

// ModelCallFilter selects calls with At in [From, To) (zero = unbounded) and,
// when BeforeID > 0, ID < BeforeID. Empty strings do not filter.
type ModelCallFilter struct {
	From, To                                time.Time
	Model, Purpose, ConversationKey, TaskID string
	BeforeID                                int64
	Limit                                   int
}

// UsageBucket sums the calls in one 15-minute UTC slot for one combination
// of dimensions. Errors counts calls whose Outcome is not OutcomeOK.
type UsageBucket struct {
	Start                                           time.Time
	BotInstanceID, Purpose, Role, Model             string
	Calls, Errors                                   int64
	PromptTokens, CompletionTokens, ReasoningTokens int64
}

// LatencySample is one successful call's latency.
type LatencySample struct {
	At                                  time.Time
	BotInstanceID, Purpose, Role, Model string
	LatencyMS                           int64
}

// PanelSession is a web panel login session. TokenHash is the SHA-256 of the
// session token, which only the cookie holds; CredFingerprint identifies the
// user's password hash at login, so changing the password ends the session.
type PanelSession struct {
	TokenHash, User, CSRFToken, CredFingerprint string
	CreatedAt, LastSeenAt, ExpiresAt            time.Time
}
