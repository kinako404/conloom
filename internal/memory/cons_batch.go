package memory

import (
	"strconv"
	"time"

	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// consBatchInput is what one batch is planned from. Messages are ordered by
// seq. Members holds the stored memory of every member that speaks, is
// replied to or is mentioned, keyed by user ID. ReplyTargets maps message IDs
// to the stored messages replied to; MentionUsers and MentionNames map
// mentioned actor IDs to user IDs and display names.
type consBatchInput struct {
	ConversationKey string
	Messages        []store.MessageRecord
	Members         map[string]store.MemberMemory
	ReplyTargets    map[string]store.MessageRecord
	MentionUsers    map[string]string
	MentionNames    map[string]string
	BotName         string
	Location        *time.Location
	BaseTokens      int
	Budget          int
	TokenFactor     float64
}

// consBatch is a planned batch: the transcript Lines of its kept messages,
// the members they show in code order, the seq consolidation reaches after
// it, how many messages it covers (Messages, including hidden ones) and how
// many of them are kept member messages, and its estimated prompt Tokens.
type consBatch struct {
	Lines          []string
	Members        []consBatchMember
	LastSeq        int64
	Messages       int
	MemberMessages int
	Tokens         int
}

// consBatchMember is a member shown as Ref. Name is their newest name in the
// batch, empty when it shows none; Memory is the row read for the batch, or
// an empty row (Version 0) when there is none.
type consBatchMember struct {
	Ref, UserID, Name string
	Memory            store.MemberMemory
}

type consAppearance struct {
	userID, name string
	spoke        bool
}

// consPlanBatch hides what forgotten members said before they were
// forgotten: their messages, their messages as reply targets and mentions of
// them made before then. It codes the remaining members P1, P2… in order of
// first appearance (speaker, reply target, then mentions) and takes messages
// while the prompt stays within Budget, always at least one kept message.
// Hidden messages up to the cut are covered by the batch too.
func consPlanBatch(in consBatchInput) consBatch {
	hidden := func(userID string, at time.Time) bool {
		m, ok := in.Members[userID]
		return ok && !m.ForgottenAt.IsZero() && at.Before(m.ForgottenAt)
	}
	targets := make(map[string]store.MessageRecord, len(in.ReplyTargets))
	for id, t := range in.ReplyTargets {
		if t.FromBot || !hidden(t.PlatformUserID, t.OccurredAt) {
			targets[id] = t
		}
	}

	refs := map[string]string{}
	userRefs := map[string]string{}
	var order []string
	kept := make([]store.MessageRecord, 0, len(in.Messages))
	keptAt := make([]int, len(in.Messages))
	seen := make([][]consAppearance, len(in.Messages))
	introduced := make([][]string, len(in.Messages))
	appear := func(i int, actorID, userID, name string, spoke bool) {
		if userID == "" {
			return
		}
		ref, ok := userRefs[userID]
		if !ok {
			ref = "P" + strconv.Itoa(len(order)+1)
			userRefs[userID] = ref
			order = append(order, userID)
			introduced[i] = append(introduced[i], userID)
		}
		refs[actorID] = ref
		seen[i] = append(seen[i], consAppearance{userID: userID, name: name, spoke: spoke})
	}
	for i, rec := range in.Messages {
		keptAt[i] = -1
		if !rec.FromBot && hidden(rec.PlatformUserID, rec.OccurredAt) {
			continue
		}
		if !rec.FromBot {
			appear(i, rec.ActorID, rec.PlatformUserID, rec.DisplayName, true)
		}
		if t, ok := targets[rec.ReplyTo]; ok && !t.FromBot {
			appear(i, t.ActorID, t.PlatformUserID, t.DisplayName, false)
		}
		if len(rec.Mentions) > 0 {
			mentions := make([]conloomapi.Mention, len(rec.Mentions))
			for j, m := range rec.Mentions {
				mentions[j] = m
				if m.Type != "user" {
					continue
				}
				userID := in.MentionUsers[m.ID]
				if userID != "" && hidden(userID, rec.OccurredAt) {
					mentions[j] = conloomapi.Mention{Type: "user"}
					continue
				}
				appear(i, m.ID, userID, in.MentionNames[m.ID], false)
			}
			rec.Mentions = mentions
		}
		keptAt[i] = len(kept)
		kept = append(kept, rec)
	}

	lines := contextbuilder.Transcript(contextbuilder.TranscriptInput{
		Messages:     kept,
		ReplyTargets: targets,
		MentionNames: in.MentionNames,
		Refs:         refs,
		BotName:      in.BotName,
		Location:     in.Location,
	})

	b := consBatch{Tokens: in.BaseTokens}
	names := map[string]string{}
	inBatch := map[string]bool{}
	for i, rec := range in.Messages {
		if k := keptAt[i]; k >= 0 {
			cost := contextbuilder.Estimate(lines[k]+"\n", in.TokenFactor)
			for _, userID := range introduced[i] {
				m := consPromptMember(in.member(userID), userRefs[userID], consFirstName(seen[i], userID))
				cost += contextbuilder.Estimate(consMemberLine(m)+"\n", in.TokenFactor)
			}
			if len(b.Lines) > 0 && b.Tokens+cost > in.Budget {
				break
			}
			b.Tokens += cost
			b.Lines = append(b.Lines, lines[k])
			if !rec.FromBot {
				b.MemberMessages++
			}
			for _, a := range seen[i] {
				inBatch[a.userID] = true
				if a.name != "" && (a.spoke || names[a.userID] == "") {
					names[a.userID] = a.name
				}
			}
		}
		b.LastSeq = rec.Seq
		b.Messages++
	}
	for _, userID := range order {
		if inBatch[userID] {
			b.Members = append(b.Members, consBatchMember{Ref: userRefs[userID], UserID: userID, Name: names[userID], Memory: in.member(userID)})
		}
	}
	return b
}

// member is userID's stored memory, or an empty row for it.
func (in consBatchInput) member(userID string) store.MemberMemory {
	if m, ok := in.Members[userID]; ok {
		return m
	}
	return store.MemberMemory{ConversationKey: in.ConversationKey, PlatformUserID: userID}
}

func consFirstName(seen []consAppearance, userID string) string {
	for _, a := range seen {
		if a.userID == userID {
			return a.name
		}
	}
	return ""
}

// consPromptMember shows m as ref under name, else its stored name, else a
// generic name.
func consPromptMember(m store.MemberMemory, ref, name string) promptMember {
	if name == "" {
		name = m.DisplayName
	}
	if name == "" {
		name = "一位群友"
	}
	return promptMember{Ref: ref, Name: name, Note: m.Note, Affinity: m.Affinity}
}
