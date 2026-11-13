package tools

import (
	"context"
	"time"

	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// TranscriptLine is a message as recall would show it: Text is its
// "[MM-DD HH:MM] 名字：正文" line, without any IDs.
type TranscriptLine struct {
	Seq  int64
	At   time.Time
	Text string
}

// Transcript applies recall's rules to msgs, a stretch of a conversation of
// convType in Seq order, with the given members: commands and the bot's
// replies to them (looking back through earlier as recall does, see
// withoutHidden) are dropped (isCommand as RecallDeps.IsCommand), so are the
// messages hidden by forgetting and those the review of pol blocks; the rest
// are redacted and reviewed and rendered in loc as recall renders them. In a
// private chat, forgetting hides everything before the latest forgetting.
func Transcript(ctx context.Context, msgs []store.MessageRecord, earlier Earlier, members []store.MemberMemory, convType string,
	pol conloomapi.GroupPolicy, reviewer OutputReviewer, isCommand func(text, prefix string) bool, loc *time.Location) ([]TranscriptLine, error) {
	t := &recallTool{d: RecallDeps{Reviewer: reviewer, IsCommand: isCommand}}
	inv := skills.Invocation{ConversationType: convType}
	if convType == conloomapi.ConvPrivate {
		var latest time.Time
		for _, m := range members {
			if m.ForgottenAt.After(latest) {
				latest, inv.PlatformUserID = m.ForgottenAt, m.PlatformUserID
			}
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	msgs, err := withoutHidden(ctx, msgs, false, earlier, t.isCommand(pol.Reply.CommandPrefix), members, inv)
	if err != nil {
		return nil, err
	}
	out := make([]TranscriptLine, 0, len(msgs))
	for _, e := range t.entries(msgs, pol) {
		out = append(out, TranscriptLine{Seq: e.seq, At: e.at, Text: recallLine(e, loc)})
	}
	return out, nil
}

// MemoryOn reports whether memory is in effect for convKey, as for recall:
// enabled, with long requests allowed. It returns the current policy too.
func MemoryOn(ctx context.Context, policies PolicySource, convKey string) (store.PolicyVersion, bool, error) {
	return memoryOn(ctx, policies, convKey)
}
