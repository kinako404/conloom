package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrInvalid wraps every rejected member memory edit; the text after its
// "memory: invalid: " prefix is meant for the admin who made the edit.
var ErrInvalid = errors.New("memory: invalid")

// ErrAudit wraps the failure to record the audit event of a change that was
// stored anyway; callers are expected to report the change as done.
var ErrAudit = errors.New("memory: audit")

// AdminDeps are the Admin's collaborators. A nil Metrics records nothing; a
// nil Now is time.Now.
type AdminDeps struct {
	Store    store.Store
	Policies *policy.Service
	Metrics  *obs.Metrics
	Now      func() time.Time
}

// Admin shows and corrects a conversation's memory on behalf of an admin, for
// both the API and the web panel. Edits and forgets work whether or not
// memory is on, so admins can correct frozen data.
type Admin struct {
	d AdminDeps
}

func NewAdmin(d AdminDeps) *Admin {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Admin{d: d}
}

// View is the conversation's memory: whether it is in effect, the summary and
// the members that have a note or an affinity.
func (a *Admin) View(ctx context.Context, conv store.ConversationRecord) (conloomapi.MemoryState, error) {
	pv, err := a.d.Policies.Current(ctx, conv.Key)
	if err != nil {
		return conloomapi.MemoryState{}, err
	}
	g, err := a.d.Store.GetGroupMemory(ctx, conv.Key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return conloomapi.MemoryState{}, err
	}
	members, err := a.d.Store.ListMemberMemories(ctx, conv.Key)
	if err != nil {
		return conloomapi.MemoryState{}, err
	}
	state := conloomapi.MemoryState{
		ConversationID: conv.ID, Enabled: pv.Policy.Memory.Enabled && pv.Policy.Concurrency.AllowLongRequests,
		Summary: g.Summary, CursorSeq: g.CursorSeq, LastError: g.LastError, Members: []conloomapi.MemberMemoryState{},
	}
	if !g.LastRunAt.IsZero() {
		at := g.LastRunAt
		state.LastRunAt = &at
	}
	for _, m := range members {
		if m.Note != "" || m.Affinity != nil {
			state.Members = append(state.Members, memberState(m))
		}
	}
	return state, nil
}

// EditMember sets userID's note, affinity or both in conv, creating the
// member's row if needed. The note is trimmed and limited by NoteLimit, the
// affinity must be in [0, 100] and at least one of them must be given;
// otherwise the error wraps ErrInvalid. Changes are recorded as revisions from
// source, counted as affinity changes from source and audited as
// "memory.update" by actor. It returns the member as stored; a failed audit
// is returned, wrapping ErrAudit, together with the member.
func (a *Admin) EditMember(ctx context.Context, conv store.ConversationRecord, userID string,
	put conloomapi.MemberMemoryPut, source, actor string) (conloomapi.MemberMemoryState, error) {
	if put.Note == nil && put.Affinity == nil {
		return conloomapi.MemberMemoryState{}, fmt.Errorf("%w: at least one of note and affinity is required", ErrInvalid)
	}
	var note string
	if put.Note != nil {
		note = strings.TrimSpace(*put.Note)
		if limit, n := NoteLimit(conv.Type), utf8.RuneCountInString(note); n > limit {
			return conloomapi.MemberMemoryState{}, fmt.Errorf("%w: note must be at most %d characters, got %d", ErrInvalid, limit, n)
		}
	}
	if put.Affinity != nil && Clamp(*put.Affinity) != *put.Affinity {
		return conloomapi.MemberMemoryState{}, fmt.Errorf("%w: affinity must be in [0, 100], got %d", ErrInvalid, *put.Affinity)
	}
	upd, err := UpdateMember(ctx, a.d.Store, conv.Key, userID, source, func(m *store.MemberMemory) (string, bool) {
		changed := false
		if put.Note != nil && m.Note != note {
			m.Note, changed = note, true
		}
		if v := put.Affinity; v != nil && (m.Affinity == nil || *m.Affinity != *v) {
			m.Affinity, changed = newAffinity(*v), true
		}
		return put.Reason, changed
	})
	if err != nil {
		return conloomapi.MemberMemoryState{}, err
	}
	var auditErr error
	if upd.Written {
		if upd.AffinityChanged() && a.d.Metrics != nil {
			a.d.Metrics.MemoryAffinityChanges.WithLabelValues(source).Inc()
		}
		var fields []string
		if upd.Before.Note != upd.After.Note {
			fields = append(fields, store.FieldNote)
		}
		if upd.AffinityChanged() {
			fields = append(fields, store.FieldAffinity)
		}
		auditErr = a.audit(ctx, conv, actor, "memory.update", struct {
			UserID string   `json:"user_id"`
			Fields []string `json:"fields"`
		}{userID, fields})
	}
	got, err := a.d.Store.GetMemberMemories(ctx, conv.Key, []string{userID})
	if err != nil {
		return conloomapi.MemberMemoryState{}, err
	}
	m, ok := got[userID]
	if !ok {
		m = upd.After
	}
	return memberState(m), auditErr
}

// ForgetMember forgets userID in conv as /忘记我 does (Forget, which also
// clears a private conversation's summary) and audits it as "memory.forget"
// by actor. A forgotten affinity is counted as an affinity change from
// store.RevisionAPI. A failed audit is returned wrapping ErrAudit; the member
// is forgotten all the same.
func (a *Admin) ForgetMember(ctx context.Context, conv store.ConversationRecord, userID, actor string) error {
	before, err := a.d.Store.GetMemberMemories(ctx, conv.Key, []string{userID})
	if err != nil {
		return err
	}
	if err := Forget(ctx, a.d.Store, conv.Key, userID, a.d.Now()); err != nil {
		return err
	}
	if before[userID].Affinity != nil && a.d.Metrics != nil {
		a.d.Metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionAPI).Inc()
	}
	return a.audit(ctx, conv, actor, "memory.forget", struct {
		UserID string `json:"user_id"`
	}{userID})
}

func (a *Admin) audit(ctx context.Context, conv store.ConversationRecord, actor, kind string, detail any) error {
	b, _ := json.Marshal(detail)
	err := a.d.Store.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: conv.BotInstanceID, Kind: kind, Actor: actor, Target: conv.Key,
		Detail: string(b), CreatedAt: a.d.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAudit, err)
	}
	return nil
}

func memberState(m store.MemberMemory) conloomapi.MemberMemoryState {
	st := conloomapi.MemberMemoryState{
		UserID: m.PlatformUserID, Name: m.DisplayName, Note: m.Note, UpdatedAt: m.UpdatedAt,
	}
	if m.Affinity != nil {
		v := *m.Affinity
		st.Affinity, st.Tier = &v, TierName(v)
	}
	return st
}
