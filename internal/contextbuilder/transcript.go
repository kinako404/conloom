package contextbuilder

import (
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// TranscriptInput describes messages to show a reader other than the bot, in
// the given order. ReplyTargets maps message IDs to the messages they reply
// to; MentionNames maps mentioned actors' IDs to their display names; Refs
// maps actor IDs to codes such as "P1". A member without a code is shown only
// as genericMemberName.
type TranscriptInput struct {
	Messages     []store.MessageRecord
	ReplyTargets map[string]store.MessageRecord
	MentionNames map[string]string
	Refs         map[string]string
	BotName      string
	Location     *time.Location
}

// Transcript renders each message as one third-person line,
// "[HH:MM] 名字（P1）（回复 …）（@了 …）：正文": the bot is "<BotName>（机器人）",
// a member with a code is "名字（P1）" and one without is genericMemberName. A
// reply target missing from ReplyTargets is "（回复了一条更早的消息）". Bodies
// are capped at HistoryMessageMaxRunes and put on one line; the bot's have
// echoed transcript prefixes stripped first. No ID is ever shown.
func Transcript(in TranscriptInput) []string {
	loc := in.Location
	if loc == nil {
		loc = defaultLocation()
	}
	names := Input{BotName: in.BotName, History: in.Messages, ReplyTargets: in.ReplyTargets, MentionNames: in.MentionNames}.SpeakerNames()
	lines := make([]string, 0, len(in.Messages))
	for _, rec := range in.Messages {
		annotation := ""
		if rec.ReplyTo != "" {
			if t, found := in.ReplyTargets[rec.ReplyTo]; found {
				t.Mentions = nil
				annotation = "（回复 " + in.label(t) + " " + clock(t, loc) + " 的「" + replyExcerpt(t, in.BotName, names, historyReplyMaxRunes) + "」）"
			} else {
				annotation = "（回复了一条更早的消息）"
			}
		}
		annotation += in.mentionAnnotation(rec)
		lines = append(lines, "["+clock(rec, loc)+"] "+in.label(rec)+annotation+"："+in.body(rec, names))
	}
	return lines
}

func (in TranscriptInput) label(rec store.MessageRecord) string {
	if rec.FromBot {
		return in.botLabel()
	}
	return in.memberLabel(rec.ActorID, displayName(rec))
}

func (in TranscriptInput) botLabel() string {
	if name := oneLine(in.BotName); name != "" {
		return name + "（机器人）"
	}
	return "机器人"
}

func (in TranscriptInput) memberLabel(actorID, name string) string {
	ref, ok := in.Refs[actorID]
	if !ok {
		return genericMemberName
	}
	return oneLine(name) + "（" + ref + "）"
}

// mentionAnnotation is "（@了 小红（P2）、一位群友）" naming, in order and once
// each, who rec mentions, or "" when it mentions nobody.
func (in TranscriptInput) mentionAnnotation(rec store.MessageRecord) string {
	var keys, labels []string
	for _, m := range rec.Mentions {
		key := m.Type + ":" + m.ID
		if m.Type == "bot" || m.Type == "all" {
			key = m.Type
		}
		if slices.Contains(keys, key) {
			continue
		}
		var label string
		switch m.Type {
		case "bot":
			label = in.botLabel()
		case "all":
			label = "全体成员"
		case "user":
			name := genericMemberName
			for _, n := range []string{in.MentionNames[m.ID], m.Name} {
				if n = mentionName(n, m.ID); n != "" {
					name = n
					break
				}
			}
			label = in.memberLabel(m.ID, name)
		default:
			continue
		}
		keys, labels = append(keys, key), append(labels, label)
	}
	if len(labels) == 0 {
		return ""
	}
	return "（@了 " + strings.Join(labels, "、") + "）"
}

// body is rec's capped body on one line. Mentions are dropped first so an
// empty body never reads "只 @ 了你".
func (in TranscriptInput) body(rec store.MessageRecord, names []string) string {
	rec.Mentions = nil
	if rec.FromBot {
		rec.Text = StripTranscriptPrefix(rec.Text, in.BotName, names...)
	}
	return oneLine(renderCappedBody(rec))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
