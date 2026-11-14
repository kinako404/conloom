package core

import "errors"

// Errors of the panel chat methods (SubmitPanel, CreatePanelChat,
// DeletePanelChat); the panel API answers them with 400.
var (
	ErrNotPrivate   = errors.New("core: panel chat needs a private conversation")
	ErrNotPanelChat = errors.New("core: not a panel chat")
	ErrNoUserYet    = errors.New("core: the conversation has no user message yet")
	ErrBadText      = errors.New("core: message text must be 1 to 2000 characters")
	ErrNotAllowed   = errors.New("core: the bot does not answer this user in private chat")
)
