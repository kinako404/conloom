package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// racingStore lets another writer change the member just before each of the
// first races writes.
type racingStore struct {
	store.Store
	t     *testing.T
	races int
}

func (s *racingStore) ApplyMemoryWrite(ctx context.Context, w store.MemoryWrite) ([]string, error) {
	if s.races > 0 {
		s.races--
		for _, m := range w.Members {
			cur := getMember(s.t, s.Store, m.ConversationKey, m.PlatformUserID)
			cur.Note += "+"
			putMember(s.t, s.Store, cur)
		}
	}
	return s.Store.ApplyMemoryWrite(ctx, w)
}

func TestUpdateMember_CreatesRowAndWritesRevisions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionRule,
		func(m *store.MemberMemory) (string, bool) {
			next, changed := ApplyInteraction(*m, "2026-11-12")
			next.DisplayName = "小明"
			*m = next
			return ReasonFirstInteraction, changed
		})
	if err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	if !u.Written || !u.AffinityChanged() || u.Before.Affinity != nil || *u.After.Affinity != 20 || u.After.Version != 1 {
		t.Fatalf("update = %+v; want a written first row at affinity 20", u)
	}
	got := getMember(t, st, testConvKey, "20001")
	if got.Affinity == nil || *got.Affinity != 20 || got.DisplayName != "小明" || got.LastInteractionDay != "2026-11-12" ||
		got.Version != 1 {
		t.Fatalf("stored = %+v; want 小明 at 20 on 2026-11-12, version 1", got)
	}
	revs, err := st.ListMemoryRevisions(ctx, testConvKey, "20001")
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions = %+v, %v; want one", revs, err)
	}
	if r := revs[0]; r.Field != store.FieldAffinity || r.OldValue != "" || r.NewValue != "20" ||
		r.Source != store.RevisionRule || r.Reason != ReasonFirstInteraction {
		t.Fatalf("revision = %+v; want affinity '' -> 20 from rule/first_interaction", r)
	}
}

func TestUpdateMember_RevisesOnlyChangedFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	putMember(t, st, store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山",
		Affinity: intPtr(40)})

	u, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionAPI, func(m *store.MemberMemory) (string, bool) {
		m.Note = "养了狗"
		return "管理员修正", true
	})
	if err != nil || !u.Written || u.AffinityChanged() {
		t.Fatalf("note update = %+v, %v; want written without an affinity change", u, err)
	}
	if _, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionRule, func(m *store.MemberMemory) (string, bool) {
		m.LastInteractionDay = "2026-11-12"
		return ReasonDaily, true
	}); err != nil {
		t.Fatalf("day update: %v", err)
	}

	revs, err := st.ListMemoryRevisions(ctx, testConvKey, "20001")
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions = %+v, %v; want only the note revision", revs, err)
	}
	if r := revs[0]; r.Field != store.FieldNote || r.OldValue != "喜欢爬山" || r.NewValue != "养了狗" ||
		r.Source != store.RevisionAPI || r.Reason != "管理员修正" {
		t.Fatalf("revision = %+v", r)
	}
	if got := getMember(t, st, testConvKey, "20001"); got.LastInteractionDay != "2026-11-12" || got.Version != 3 {
		t.Fatalf("stored = %+v; want the day written at version 3", got)
	}
}

func TestUpdateMember_DeclinedWritesNothing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	u, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionRule, func(m *store.MemberMemory) (string, bool) {
		m.Affinity = intPtr(50)
		return ReasonDecay, false
	})
	if err != nil || u.Written || u.AffinityChanged() {
		t.Fatalf("declined update = %+v, %v; want nothing written", u, err)
	}
	if got, err := st.GetMemberMemories(ctx, testConvKey, []string{"20001"}); err != nil || len(got) != 0 {
		t.Fatalf("rows = %+v, %v; want none", got, err)
	}
}

func TestUpdateMember_RetriesAfterConflict(t *testing.T) {
	db := newTestStore(t)
	putMember(t, db, store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山",
		Affinity: intPtr(40)})
	st := &racingStore{Store: db, t: t, races: 2}

	calls := 0
	u, err := UpdateMember(context.Background(), st, testConvKey, "20001", store.RevisionAPI,
		func(m *store.MemberMemory) (string, bool) {
			calls++
			*m.Affinity += 5
			return "", true
		})
	if err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	if calls != 3 {
		t.Fatalf("edit ran %d times, want 3", calls)
	}
	got := getMember(t, db, testConvKey, "20001")
	if got.Note != "喜欢爬山++" || got.Affinity == nil || *got.Affinity != 45 || got.Version != 4 {
		t.Fatalf("stored = %+v; want both racing notes kept and affinity 45 at version 4", got)
	}
	if *u.Before.Affinity != 40 || *u.After.Affinity != 45 || u.After.Version != 4 {
		t.Fatalf("update = %+v; want 40 -> 45 at version 4", u)
	}
	revs, err := db.ListMemoryRevisions(context.Background(), testConvKey, "20001")
	if err != nil || len(revs) != 1 || revs[0].OldValue != "40" || revs[0].NewValue != "45" {
		t.Fatalf("revisions = %+v, %v; want one 40 -> 45", revs, err)
	}
}

func TestUpdateMember_GivesUpAfterThreeConflicts(t *testing.T) {
	db := newTestStore(t)
	putMember(t, db, store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山",
		Affinity: intPtr(40)})
	st := &racingStore{Store: db, t: t, races: 3}

	calls := 0
	_, err := UpdateMember(context.Background(), st, testConvKey, "20001", store.RevisionAPI,
		func(m *store.MemberMemory) (string, bool) {
			calls++
			*m.Affinity += 5
			return "", true
		})
	if !errors.Is(err, ErrUpdateConflict) {
		t.Fatalf("UpdateMember error = %v, want ErrUpdateConflict", err)
	}
	if calls != 3 {
		t.Fatalf("edit ran %d times, want 3", calls)
	}
	if got := getMember(t, db, testConvKey, "20001"); *got.Affinity != 40 || got.Note != "喜欢爬山+++" {
		t.Fatalf("stored = %+v; want only the racing writes", got)
	}
}

func TestUpdateMember_KeepsForgottenAt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	if err := st.ForgetMember(ctx, testConvKey, "20001", at); err != nil {
		t.Fatalf("ForgetMember: %v", err)
	}

	if _, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionRule, func(m *store.MemberMemory) (string, bool) {
		next, changed := ApplyInteraction(*m, "2026-11-12")
		*m = next
		return ReasonFirstInteraction, changed
	}); err != nil {
		t.Fatalf("interaction: %v", err)
	}
	if got := getMember(t, st, testConvKey, "20001"); !got.ForgottenAt.Equal(at) || got.Affinity == nil || *got.Affinity != 20 {
		t.Fatalf("after interaction = %+v; want affinity 20 and ForgottenAt %v", got, at)
	}

	if _, err := UpdateMember(ctx, st, testConvKey, "20001", store.RevisionAPI, func(m *store.MemberMemory) (string, bool) {
		*m = store.MemberMemory{Note: "养了狗", Affinity: intPtr(50)}
		return "", true
	}); err != nil {
		t.Fatalf("replacing edit: %v", err)
	}
	got := getMember(t, st, testConvKey, "20001")
	if !got.ForgottenAt.Equal(at) || got.Note != "养了狗" || *got.Affinity != 50 || got.Version != 3 {
		t.Fatalf("after replacing edit = %+v; want 养了狗 at 50, version 3, ForgottenAt %v", got, at)
	}
}

func TestUpdateMember_EditCannotChangeInputAffinity(t *testing.T) {
	st := newTestStore(t)
	putMember(t, st, store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", Affinity: intPtr(40)})

	u, err := UpdateMember(context.Background(), st, testConvKey, "20001", store.RevisionAPI,
		func(m *store.MemberMemory) (string, bool) {
			*m.Affinity = 60
			return "", true
		})
	if err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	if *u.Before.Affinity != 40 || *u.After.Affinity != 60 || !u.AffinityChanged() {
		t.Fatalf("update = before %d, after %d; want 40 -> 60", *u.Before.Affinity, *u.After.Affinity)
	}
}
