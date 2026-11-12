package memory

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/kinako404/conloom/internal/store"
)

// ErrUpdateConflict is returned by UpdateMember when the member kept changing
// under it.
var ErrUpdateConflict = errors.New("member memory changed concurrently")

const updateAttempts = 3

// MemberStore is the part of store.Store that UpdateMember uses.
type MemberStore interface {
	GetMemberMemories(ctx context.Context, convKey string, userIDs []string) (map[string]store.MemberMemory, error)
	ApplyMemoryWrite(ctx context.Context, w store.MemoryWrite) (conflicts []string, err error)
}

// MemberUpdate is the member as read before an UpdateMember edit and as
// written after it. After equals Before when nothing was written.
type MemberUpdate struct {
	Before, After store.MemberMemory
	Written       bool
}

// AffinityChanged reports whether the written update changed the affinity.
func (u MemberUpdate) AffinityChanged() bool {
	return u.Written && affinityValue(u.Before.Affinity) != affinityValue(u.After.Affinity)
}

// UpdateMember reads the user's memory row (an empty one at version 0 if none
// exists), lets edit change it, and writes it back if edit returns write. The
// write records a revision with source and edit's reason for each of the note
// and affinity that changed. Identity, version and ForgottenAt are kept from
// the row read whatever edit does. On a version conflict it reads again and
// reruns edit, up to 3 attempts in all, then returns ErrUpdateConflict.
func UpdateMember(ctx context.Context, st MemberStore, convKey, userID, source string,
	edit func(m *store.MemberMemory) (reason string, write bool)) (MemberUpdate, error) {
	for range updateAttempts {
		got, err := st.GetMemberMemories(ctx, convKey, []string{userID})
		if err != nil {
			return MemberUpdate{}, err
		}
		before, ok := got[userID]
		if !ok {
			before = store.MemberMemory{ConversationKey: convKey, PlatformUserID: userID}
		}
		after := before
		if before.Affinity != nil {
			after.Affinity = newAffinity(*before.Affinity)
		}
		reason, write := edit(&after)
		if !write {
			return MemberUpdate{Before: before, After: before}, nil
		}
		after.ConversationKey = before.ConversationKey
		after.PlatformUserID = before.PlatformUserID
		after.Version = before.Version
		after.ForgottenAt = before.ForgottenAt
		conflicts, err := st.ApplyMemoryWrite(ctx, store.MemoryWrite{
			ConversationKey: convKey,
			Members:         []store.MemberMemory{after},
			Revisions:       memberRevisions(before, after, source, reason),
		})
		if err != nil {
			return MemberUpdate{}, err
		}
		if len(conflicts) == 0 {
			after.Version++
			return MemberUpdate{Before: before, After: after, Written: true}, nil
		}
	}
	return MemberUpdate{}, fmt.Errorf("update memory of %s in %s: %w", userID, convKey, ErrUpdateConflict)
}

func memberRevisions(before, after store.MemberMemory, source, reason string) []store.MemoryRevision {
	var revs []store.MemoryRevision
	add := func(field, oldValue, newValue string) {
		if oldValue != newValue {
			revs = append(revs, store.MemoryRevision{ConversationKey: after.ConversationKey,
				PlatformUserID: after.PlatformUserID, Field: field, OldValue: oldValue, NewValue: newValue,
				Source: source, Reason: reason})
		}
	}
	add(store.FieldNote, before.Note, after.Note)
	add(store.FieldAffinity, affinityValue(before.Affinity), affinityValue(after.Affinity))
	return revs
}

func affinityValue(a *int) string {
	if a == nil {
		return ""
	}
	return strconv.Itoa(*a)
}
