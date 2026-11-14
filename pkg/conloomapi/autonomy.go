package conloomapi

import "time"

// AutonomyState is the hidden state of a private chat as the panel and the
// API show it: the status while it lasts (null otherwise), the persona's bars
// in its order at their values decayed to now, the chat's newest bar changes
// first and how the bot starts the chat. Activity and Reason are text the
// model or an operator wrote; show them as plain text.
type AutonomyState struct {
	Status    *AutonomyStatus    `json:"status"`
	Bars      []AutonomyBar      `json:"bars"`
	Events    []AutonomyBarEvent `json:"events"`
	Proactive AutonomyProactive  `json:"proactive"`
}

// AutonomyStatus is what the bot is doing in a private chat, from StartedAt
// until UntilAt.
type AutonomyStatus struct {
	Activity  string    `json:"activity"`
	StartedAt time.Time `json:"started_at"`
	UntilAt   time.Time `json:"until_at"`
}

// AutonomyBar is one of the persona's hidden bars: its value decayed to now,
// rounded, the band of that value (很低, 偏低, 中等, 偏高 or 很高), and the
// bar's baseline and description.
type AutonomyBar struct {
	Name        string `json:"name"`
	Value       int    `json:"value"`
	Band        string `json:"band"`
	Baseline    int    `json:"baseline"`
	Description string `json:"description,omitempty"`
}

// AutonomyBarEvent is one change of a bar from Before to After, both rounded,
// at At. Source is model, panel or api.
type AutonomyBarEvent struct {
	Name   string    `json:"name"`
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	Before int       `json:"before"`
	After  int       `json:"after"`
	At     time.Time `json:"at"`
}

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

// AutonomyBarPut sets a bar of a private chat to Value, an integer 0–100,
// regardless of the limits on the model's changes; Reason, at most 40
// characters, may be empty.
type AutonomyBarPut struct {
	Value  *int   `json:"value"`
	Reason string `json:"reason"`
}
