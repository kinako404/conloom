package conloomapi

import "time"

// AutonomyProactive describes how the bot starts a private chat: whether
// proactive replies are on, the effective private level, how many messages
// the bot started today out of the level's daily cap, the quiet hours in
// effect ("HH:MM" in server.timezone; QuietInferred when computed from the
// user's messages), how many more messages it may start before the user
// writes again, and the chat's pending timers ordered by DueAt.
type AutonomyProactive struct {
	Enabled        bool            `json:"enabled"`
	Level          string          `json:"level"`
	SentToday      int             `json:"sent_today"`
	DailyCap       int             `json:"daily_cap"`
	QuietStart     string          `json:"quiet_start"`
	QuietEnd       string          `json:"quiet_end"`
	QuietInferred  bool            `json:"quiet_inferred"`
	UnansweredLeft int             `json:"unanswered_left"`
	Timers         []AutonomyTimer `json:"timers"`
}

// AutonomyTimer is a pending wake-up of a private chat: Kind is status_return,
// initiate or follow_up.
type AutonomyTimer struct {
	Kind  string    `json:"kind"`
	DueAt time.Time `json:"due_at"`
}
