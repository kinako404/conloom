package core

import "errors"

// Errors of the panel chat methods (SubmitPanel, CreatePanelChat,
// DeletePanelChat); the panel API answers them with 400. AutonomyService also
// refuses conversations that are not private with ErrNotPrivate.
var (
	ErrNotPrivate   = errors.New("core: panel chat needs a private conversation")
	ErrNotPanelChat = errors.New("core: not a panel chat")
	ErrBadText      = errors.New("core: message text must be 1 to 2000 characters")
)

// ErrChatBusy refuses to delete a panel chat whose cancelled tasks did not
// stop in time; the panel API answers it with 409.
var ErrChatBusy = errors.New("core: the chat's cancelled tasks are still stopping; try again")

// ErrChatNotIdle refuses to withdraw or regenerate while the bot is still
// replying or has parts of a reply left to deliver; the panel API answers it
// with 409.
var ErrChatNotIdle = errors.New("core: the chat is still replying; try again after it finishes")

// ErrNotLatest refuses to regenerate anything but the newest bot reply; the
// panel API answers it with 400.
var ErrNotLatest = errors.New("core: only the newest bot reply can be regenerated")

// ErrCommandReply refuses to regenerate the reply to a command; the panel API
// answers it with 400.
var ErrCommandReply = errors.New("core: a command's reply cannot be regenerated")

// ErrRegenMerged stops a regenerate that the scheduler would merge into a
// task of the same actor created meanwhile; the panel API answers it with 409.
var ErrRegenMerged = errors.New("core: the chat changed while regenerating; try again")
