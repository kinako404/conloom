package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const panelBot = "qq-main"

func seedConversations(t *testing.T, db *DB, convs ...store.ConversationRecord) {
	t.Helper()
	ctx := context.Background()
	if err := db.inTx(ctx, func(tx *sql.Tx) error {
		for _, c := range convs {
			if err := upsertConversation(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}
}

func panelConv(platform, convType, platformID string) store.ConversationRecord {
	id := "conv_" + platformID
	return store.ConversationRecord{
		BotInstanceID: panelBot, ID: id, Key: store.ConversationKey(panelBot, platform, convType, id),
		Type: convType, Platform: platform, PlatformConversationID: platformID,
	}
}

func panelMessage(convKey string, at time.Time) store.MessageRecord {
	m := memMessage(convKey, "user_20001", "20001", false)
	m.DisplayName, m.OccurredAt, m.CreatedAt = "小明", at, at
	return m
}

func seqs(msgs []store.MessageRecord) []int64 {
	out := []int64{}
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

func TestGetConversationByKey(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tg := store.ConversationRecord{
		BotInstanceID: panelBot, ID: "-1001234", Key: "qq-main:telegram:group:-1001234",
		Type: conloomapi.ConvGroup, Platform: "telegram", PlatformConversationID: "-1001234",
	}
	seedConversations(t, db, tg, panelConv("qq", conloomapi.ConvGroup, "10001"))
	got, err := db.GetConversationByKey(ctx, "qq-main:telegram:group:-1001234")
	if err != nil || got != tg {
		t.Fatalf("GetConversationByKey = %+v, %v; want %+v", got, err, tg)
	}
	if _, err := db.GetConversationByKey(ctx, "qq-main:telegram:group:-999"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConversationByKey missing err = %v, want ErrNotFound", err)
	}
}

func TestConversationStats(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	empty := panelConv("qq", conloomapi.ConvGroup, "10000")
	busy := panelConv("qq", conloomapi.ConvGroup, "10001")
	older := panelConv("qq", conloomapi.ConvPrivate, "20001")
	seedConversations(t, db, older, empty, busy)
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	memInsertMessages(t, db,
		panelMessage(older.Key, base.Add(-time.Hour)),
		panelMessage(busy.Key, base),
		panelMessage(busy.Key, base.Add(time.Minute)),
		panelMessage(busy.Key, base.Add(2*time.Minute)),
	)

	got, err := db.ConversationStats(ctx)
	if err != nil {
		t.Fatalf("ConversationStats: %v", err)
	}
	want := []store.ConversationStats{
		{ConversationRecord: busy, MessageCount: 3, LastMessageAt: base.Add(2 * time.Minute)},
		{ConversationRecord: older, MessageCount: 1, LastMessageAt: base.Add(-time.Hour)},
		{ConversationRecord: empty},
	}
	if len(got) != len(want) {
		t.Fatalf("ConversationStats = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ConversationRecord != want[i].ConversationRecord || got[i].MessageCount != want[i].MessageCount ||
			!got[i].LastMessageAt.Equal(want[i].LastMessageAt) {
			t.Errorf("stats[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !got[2].LastMessageAt.IsZero() {
		t.Errorf("empty conversation LastMessageAt = %v, want zero", got[2].LastMessageAt)
	}

	fresh := newTestDB(t)
	if got, err := fresh.ConversationStats(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ConversationStats on empty db = %+v, %v; want none", got, err)
	}
}

func TestMessagesBefore_Paging(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	conv := panelConv("qq", conloomapi.ConvGroup, "10001")
	other := panelConv("qq", conloomapi.ConvGroup, "10002")
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	var mine []store.MessageRecord
	for i := range 5 {
		m := panelMessage(conv.Key, base.Add(time.Duration(i)*time.Minute))
		m.Mentions = []conloomapi.Mention{{Type: "user", ID: "20002", Name: "小红"}}
		mine = append(mine, m)
		memInsertMessages(t, db, m, panelMessage(other.Key, base))
	}
	all, err := db.MessagesBefore(ctx, conv.Key, 0, 200)
	if err != nil || len(all) != 5 {
		t.Fatalf("MessagesBefore(0, 200) = %d, %v; want 5", len(all), err)
	}
	seq := map[int]int64{}
	for i, m := range all {
		n := 5 - i
		if m.ID != mine[n-1].ID || m.ConversationKey != conv.Key || !reflect.DeepEqual(m.Mentions, mine[n-1].Mentions) {
			t.Fatalf("message %d = %+v, want %s", i, m, mine[n-1].ID)
		}
		seq[n] = m.Seq
	}

	got, err := db.MessagesBefore(ctx, conv.Key, 0, 2)
	if want := []int64{seq[5], seq[4]}; err != nil || !reflect.DeepEqual(seqs(got), want) {
		t.Fatalf("MessagesBefore(0, 2) = %v, %v; want %v", seqs(got), err, want)
	}
	got, err = db.MessagesBefore(ctx, conv.Key, seq[4], 2)
	if want := []int64{seq[3], seq[2]}; err != nil || !reflect.DeepEqual(seqs(got), want) {
		t.Fatalf("MessagesBefore(seq4, 2) = %v, %v; want %v", seqs(got), err, want)
	}
	got, err = db.MessagesBefore(ctx, conv.Key, seq[2], 2)
	if want := []int64{seq[1]}; err != nil || !reflect.DeepEqual(seqs(got), want) {
		t.Fatalf("MessagesBefore(seq2, 2) = %v, %v; want %v", seqs(got), err, want)
	}
	if got, err := db.MessagesBefore(ctx, conv.Key, 0, 0); err != nil || len(got) != 0 {
		t.Fatalf("MessagesBefore limit 0 = %d, %v; want none", len(got), err)
	}
}

func TestMessagesBefore_LimitCapped(t *testing.T) {
	db := newTestDB(t)
	conv := panelConv("qq", conloomapi.ConvGroup, "10001")
	var msgs []store.MessageRecord
	for range 205 {
		msgs = append(msgs, panelMessage(conv.Key, time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)))
	}
	memInsertMessages(t, db, msgs...)
	got, err := db.MessagesBefore(context.Background(), conv.Key, 0, 1000)
	if err != nil || len(got) != 200 {
		t.Fatalf("MessagesBefore(limit 1000) = %d, %v; want 200", len(got), err)
	}
}

func TestMessagesBySeq_ScopedToConversation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	conv := panelConv("qq", conloomapi.ConvGroup, "10001")
	other := panelConv("qq", conloomapi.ConvGroup, "10002")
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	a, b, c := panelMessage(conv.Key, at), panelMessage(other.Key, at), panelMessage(conv.Key, at)
	memInsertMessages(t, db, a, b, c)
	sa, sb, sc := seqOf(t, db, a.ID), seqOf(t, db, b.ID), seqOf(t, db, c.ID)

	got, err := db.MessagesBySeq(ctx, conv.Key, []int64{sa, sb, sc, 99999})
	if err != nil {
		t.Fatalf("MessagesBySeq: %v", err)
	}
	if len(got) != 2 || got[sa].ID != a.ID || got[sc].ID != c.ID {
		t.Fatalf("MessagesBySeq = %+v, want %s and %s only", got, a.ID, c.ID)
	}
	if _, ok := got[sb]; ok {
		t.Fatalf("MessagesBySeq returned other conversation's seq %d", sb)
	}
	if got, err := db.MessagesBySeq(ctx, conv.Key, nil); err != nil || len(got) != 0 {
		t.Fatalf("MessagesBySeq(nil) = %+v, %v; want none", got, err)
	}
}

func seedPanelTasks(t *testing.T, db *DB, tasks ...store.TaskRecord) {
	t.Helper()
	ctx := context.Background()
	if err := db.inTx(ctx, func(tx *sql.Tx) error {
		for _, tk := range tasks {
			if err := insertTask(ctx, tx, tk); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
}

func taskIDs(tasks []store.TaskRecord) []string {
	out := []string{}
	for _, tk := range tasks {
		out = append(out, tk.ID)
	}
	return out
}

func TestListTasks_FiltersAndCursor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convA := store.ConversationKey(panelBot, "qq", conloomapi.ConvGroup, "conv_10001")
	convB := store.ConversationKey(panelBot, "qq", conloomapi.ConvGroup, "conv_10002")
	convC := store.ConversationKey("qq-alt", "qq", conloomapi.ConvGroup, "conv_10001")
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	task := func(id, bot, conv, status string) store.TaskRecord {
		return store.TaskRecord{
			ID: id, BotInstanceID: bot, ConversationKey: conv, Status: status,
			TriggerMessageIDs: []string{"msg_" + id}, Meta: conloomapi.TaskMeta{RouteReason: "mention"},
			EventOccurredAt: at, CreatedAt: at, UpdatedAt: at,
		}
	}
	seedPanelTasks(t, db,
		task("task_01", panelBot, convA, conloomapi.TaskSucceeded),
		task("task_02", panelBot, convB, conloomapi.TaskFailed),
		task("task_03", "qq-alt", convC, conloomapi.TaskSucceeded),
		task("task_04", panelBot, convA, conloomapi.TaskFailed),
		task("task_05", panelBot, convA, conloomapi.TaskSucceeded),
	)

	for name, tc := range map[string]struct {
		f    store.TaskFilter
		want []string
	}{
		"all":          {store.TaskFilter{Limit: 50}, []string{"task_05", "task_04", "task_03", "task_02", "task_01"}},
		"status":       {store.TaskFilter{Status: conloomapi.TaskFailed, Limit: 50}, []string{"task_04", "task_02"}},
		"bot":          {store.TaskFilter{BotInstanceID: "qq-alt", Limit: 50}, []string{"task_03"}},
		"conversation": {store.TaskFilter{ConversationKey: convA, Limit: 50}, []string{"task_05", "task_04", "task_01"}},
		"before":       {store.TaskFilter{Before: "task_04", Limit: 50}, []string{"task_03", "task_02", "task_01"}},
		"limit":        {store.TaskFilter{Limit: 2}, []string{"task_05", "task_04"}},
		"zero limit":   {store.TaskFilter{}, []string{}},
		"combined": {store.TaskFilter{Status: conloomapi.TaskSucceeded, BotInstanceID: panelBot, ConversationKey: convA,
			Before: "task_05", Limit: 50}, []string{"task_01"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := db.ListTasks(ctx, tc.f)
			if err != nil || !reflect.DeepEqual(taskIDs(got), tc.want) {
				t.Fatalf("ListTasks(%+v) = %v, %v; want %v", tc.f, taskIDs(got), err, tc.want)
			}
		})
	}

	got, err := db.ListTasks(ctx, store.TaskFilter{Limit: 1})
	if err != nil || len(got) != 1 {
		t.Fatalf("ListTasks(limit 1) = %v, %v", got, err)
	}
	want := task("task_05", panelBot, convA, conloomapi.TaskSucceeded)
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("task = %+v\nwant %+v", got[0], want)
	}
}

func TestListTasks_LimitCapped(t *testing.T) {
	db := newTestDB(t)
	var tasks []store.TaskRecord
	for i := range 205 {
		tasks = append(tasks, store.TaskRecord{
			ID: fmt.Sprintf("task_%03d", i), BotInstanceID: panelBot, ConversationKey: testConvKey,
			Status: conloomapi.TaskSucceeded, TriggerMessageIDs: []string{},
		})
	}
	seedPanelTasks(t, db, tasks...)
	got, err := db.ListTasks(context.Background(), store.TaskFilter{Limit: 1000})
	if err != nil || len(got) != 200 || got[0].ID != "task_204" {
		t.Fatalf("ListTasks(limit 1000) = %d, %v; want 200 newest first", len(got), err)
	}
}

func auditIDs(events []store.AuditEvent) []string {
	out := []string{}
	for _, a := range events {
		out = append(out, a.ID)
	}
	return out
}

func TestListAudit_FiltersAndCursor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	events := []store.AuditEvent{
		{ID: "aud_01", BotInstanceID: panelBot, Kind: "persona.update", Actor: "panel:admin", Target: testConvKey, Detail: "<script>x</script>", CreatedAt: at},
		{ID: "aud_02", BotInstanceID: panelBot, Kind: "policy.update", Actor: "panel:admin", Target: testConvKey, CreatedAt: at},
		{ID: "aud_03", BotInstanceID: "qq-alt", Kind: "persona.update", Actor: "20001", Target: testConvKey, CreatedAt: at},
		{ID: "aud_04", BotInstanceID: panelBot, Kind: "persona.update", Actor: "20001", Target: testConvKey, CreatedAt: at},
	}
	for _, a := range events {
		if err := db.InsertAudit(ctx, a); err != nil {
			t.Fatalf("InsertAudit: %v", err)
		}
	}

	for name, tc := range map[string]struct {
		f    store.AuditFilter
		want []string
	}{
		"all":        {store.AuditFilter{Limit: 50}, []string{"aud_04", "aud_03", "aud_02", "aud_01"}},
		"kind":       {store.AuditFilter{Kind: "persona.update", Limit: 50}, []string{"aud_04", "aud_03", "aud_01"}},
		"bot":        {store.AuditFilter{BotInstanceID: "qq-alt", Limit: 50}, []string{"aud_03"}},
		"before":     {store.AuditFilter{Before: "aud_03", Limit: 50}, []string{"aud_02", "aud_01"}},
		"limit":      {store.AuditFilter{Limit: 3}, []string{"aud_04", "aud_03", "aud_02"}},
		"zero limit": {store.AuditFilter{}, []string{}},
		"combined": {store.AuditFilter{Kind: "persona.update", BotInstanceID: panelBot, Before: "aud_04", Limit: 50},
			[]string{"aud_01"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := db.ListAudit(ctx, tc.f)
			if err != nil || !reflect.DeepEqual(auditIDs(got), tc.want) {
				t.Fatalf("ListAudit(%+v) = %v, %v; want %v", tc.f, auditIDs(got), err, tc.want)
			}
		})
	}

	got, err := db.ListAudit(ctx, store.AuditFilter{Before: "aud_02", Limit: 1})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], events[0]) {
		t.Fatalf("ListAudit = %+v, %v; want %+v", got, err, events[0])
	}
}

func TestListPersonaVersions_NewestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for i, name := range []string{"小明一号", "小明二号", "小明三号"} {
		_, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
			ConversationKey: testConvKey, TemplateName: "default", Persona: conloomapi.PersonaDoc{PersonaName: name},
			Reason: fmt.Sprintf("v%d", i+1), UpdatedBy: "panel:admin", CreatedAt: at.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("AppendPersonaVersion: %v", err)
		}
	}
	if _, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
		ConversationKey: testConvKeyB, TemplateName: "default", Persona: conloomapi.PersonaDoc{PersonaName: "别的"},
	}); err != nil {
		t.Fatalf("AppendPersonaVersion other: %v", err)
	}
	got, err := db.ListPersonaVersions(ctx, testConvKey)
	if err != nil || len(got) != 3 {
		t.Fatalf("ListPersonaVersions = %+v, %v; want 3", got, err)
	}
	for i, pv := range got {
		want, err := db.GetPersonaVersion(ctx, testConvKey, 3-i)
		if err != nil {
			t.Fatalf("GetPersonaVersion: %v", err)
		}
		if !reflect.DeepEqual(pv, want) {
			t.Fatalf("version[%d] = %+v, want %+v", i, pv, want)
		}
	}
	if got, err := db.ListPersonaVersions(ctx, "qq-main:qq:group:none"); err != nil || len(got) != 0 {
		t.Fatalf("ListPersonaVersions(missing) = %+v, %v; want none", got, err)
	}
}

func TestCountPendingOutbox(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if n, err := db.CountPendingOutbox(ctx); err != nil || n != 0 {
		t.Fatalf("CountPendingOutbox on empty db = %d, %v; want 0", n, err)
	}
	now := time.Now().UTC()
	insertActions(t, db,
		taskAction("act_pending_1", "task_1", store.OutboxPending, now),
		taskAction("act_pending_2", "task_1", store.OutboxPending, now),
		taskAction("act_leased", "task_1", store.OutboxLeased, now),
		taskAction("act_acked", "task_1", store.OutboxAcked, now),
		taskAction("act_expired", "task_1", store.OutboxExpired, now),
		taskAction("act_cancelled", "task_1", store.OutboxCancelled, now),
	)
	if n, err := db.CountPendingOutbox(ctx); err != nil || n != 3 {
		t.Fatalf("CountPendingOutbox = %d, %v; want 3", n, err)
	}
}
