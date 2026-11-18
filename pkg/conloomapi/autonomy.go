package conloomapi

import "time"

// AutonomyState is the hidden state of a private chat as the panel and the
// API show it: the status while it lasts (null otherwise), the bars (the
// persona's in its order, then those the bot added) at their values decayed
// to now, the names of the bars the bot added that a persona bar of the same
// name has since replaced (ShadowedBars: the bot no longer sees them, but
// each still counts toward its limit of bars until removed), the effects
// still active, the chat's newest bar and effect changes first and how the
// bot starts the chat. Activity, Reason, and the names and descriptions the
// bot gave its bars and effects are text the model or an operator wrote;
// show them as plain text.
type AutonomyState struct {
	Status       *AutonomyStatus    `json:"status"`
	Bars         []AutonomyBar      `json:"bars"`
	ShadowedBars []string           `json:"shadowed_bars"`
	Effects      []AutonomyEffect   `json:"effects"`
	Events       []AutonomyBarEvent `json:"events"`
	Proactive    AutonomyProactive  `json:"proactive"`
}

// AutonomyStatus is what the bot is doing in a private chat, from StartedAt
// until UntilAt.
type AutonomyStatus struct {
	Activity  string    `json:"activity"`
	StartedAt time.Time `json:"started_at"`
	UntilAt   time.Time `json:"until_at"`
}

// The modes of a private chat's scene, and how many runes its place has at
// most.
const (
	SceneOnline        = "online"
	SceneInPerson      = "in_person"
	ScenePlaceMaxRunes = 16
)

// AutonomyScene is a private chat's scene as in effect now: online, or in
// person at Place (empty when none was given) since SinceAt (zero for a chat
// that never had a scene). Enabled: the bot enables the scene skill.
// IdleOnline: online because the person said nothing for 3 hours while in
// person. Place is text the model or an operator wrote; show it as plain text.
type AutonomyScene struct {
	Enabled    bool      `json:"enabled"`
	Mode       string    `json:"mode"`
	Place      string    `json:"place"`
	SinceAt    time.Time `json:"since_at,omitzero"`
	IdleOnline bool      `json:"idle_online"`
}

// AutonomyScenePut sets a private chat's scene: Mode is SceneOnline or
// SceneInPerson, Place at most ScenePlaceMaxRunes runes, ignored online.
type AutonomyScenePut struct {
	Mode  string `json:"mode"`
	Place string `json:"place"`
}

// AutonomyBar is one of the hidden bars: its value decayed to now, rounded,
// the band of that value (很低, 偏低, 中等, 偏高 or 很高), and the bar's
// baseline and description. Custom marks a bar the bot added itself
// (add_buff), which can be removed; the persona's bars cannot.
type AutonomyBar struct {
	Name        string `json:"name"`
	Value       int    `json:"value"`
	Band        string `json:"band"`
	Baseline    int    `json:"baseline"`
	Description string `json:"description,omitempty"`
	Custom      bool   `json:"custom"`
}

// AutonomyEffect is an effect the bot gave itself (add_buff), active from
// StartedAt until UntilAt.
type AutonomyEffect struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UntilAt     time.Time `json:"until_at"`
}

// AutonomyBarEvent is one change of a bar or an effect at At. Kind is adjust
// (a bar's value from Before to After, both rounded), bar_add (After is the
// new bar's value), bar_remove (Before and After are its value as last
// stored), effect_add or effect_remove (both 0). Source is model, panel or
// api.
type AutonomyBarEvent struct {
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
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
// user's messages), how many more messages it may send on its own before the
// user writes again (a sent status return uses one up but does not count
// toward the daily cap), and the chat's pending timers ordered by DueAt.
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
