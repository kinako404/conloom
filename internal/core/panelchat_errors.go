package core

import (
	"errors"
	"fmt"

	"github.com/kinako404/conloom/internal/store"
)

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

// ErrNotLatest refuses to regenerate anything but the chat's newest message
// when that is from the bot; the panel API answers it with 400.
var ErrNotLatest = errors.New("core: only the newest bot reply can be regenerated")

// ErrNotReply refuses to regenerate a bot message that is not part of the
// bot's ordinary reply to the user's messages just before it, such as one it
// sent on its own (a wake or proactive message); the panel API answers it
// with 400.
var ErrNotReply = errors.New("core: only the bot's reply to the messages just before it can be regenerated, not a message it sent on its own")

// ErrCommandReply refuses to regenerate the reply to a command; the panel API
// answers it with 400.
var ErrCommandReply = errors.New("core: a command's reply cannot be regenerated")

// ErrForgotten refuses to regenerate a reply to messages sent before their
// sender was forgotten, which a new reply would leave out; the panel API
// answers it with 400.
var ErrForgotten = errors.New("core: the messages this reply answered were forgotten")

// ErrMessageNotFound is a seq that is not a message of the chat. It is a
// store.ErrNotFound; the panel API answers it with 404 MESSAGE_NOT_FOUND,
// unlike a missing chat.
var ErrMessageNotFound = fmt.Errorf("%w: no such message in the chat", store.ErrNotFound)

// ErrRegenMerged stops a regenerate that the scheduler would merge into a
// task of the same actor created meanwhile; the panel API answers it with 409.
var ErrRegenMerged = errors.New("core: the chat changed while regenerating; try again")
