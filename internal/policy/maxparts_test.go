package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// replyJSON returns p as JSON with reply.max_parts set to maxParts, or left
// out when it is empty.
func replyJSON(t *testing.T, p conloomapi.GroupPolicy, maxParts string) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["reply"], "max_parts")
	if maxParts != "" {
		doc["reply"]["max_parts"] = json.RawMessage(maxParts)
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPolicy_StoredMaxPartsMissingOrZero(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()

	stored := conloomapi.DefaultGroupPolicy()
	stored.Context.MaxRecentMessages = 30
	rows := map[string]string{
		testPrivateKey: replyJSON(t, stored, ""),
		testConvKey:    replyJSON(t, stored, "0"),
	}
	for key, raw := range rows {
		if _, err := conn.ExecContext(ctx, `INSERT INTO group_policies
			(conversation_key, version, policy, reason, updated_by, created_at) VALUES (?, 3, ?, 'legacy', '29999', ?)`,
			key, raw, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
	}

	group := conloomapi.DefaultGroupPolicy()
	group.Reply.MaxParts = 2
	private := privateDefaults()
	private.Reply.MaxParts = 4
	s := NewService(db, group).WithPrivateDefaults(private)

	pv, err := s.Current(ctx, testPrivateKey)
	if err != nil || pv.Policy.Reply.MaxParts != 4 || pv.Policy.Context.MaxRecentMessages != 30 {
		t.Fatalf("private Current without max_parts = %+v, %v; want the private default 4", pv.Policy.Reply, err)
	}
	pv, err = s.Current(ctx, testConvKey)
	if err != nil || pv.Policy.Reply.MaxParts != 0 {
		t.Fatalf("group Current with max_parts 0 = %+v, %v; want 0 kept", pv.Policy.Reply, err)
	}

	paused, err := s.Pause(ctx, testPrivateKey, "29999")
	if err != nil || paused.Policy.Reply.MaxParts != 4 {
		t.Fatalf("Pause = %+v, %v; want max_parts 4 stored", paused.Policy.Reply, err)
	}
	versions, err := db.ListPolicyVersions(ctx, testPrivateKey)
	if err != nil || len(versions) != 2 || versions[0].Policy.Reply.MaxParts != 4 {
		t.Fatalf("versions = %+v, %v; want the paused version with max_parts 4 first", versions, err)
	}
	if err := Validate(versions[0].Policy); err != nil {
		t.Fatalf("stored paused version invalid: %v", err)
	}
}

func TestPolicy_UpdateMaxPartsMissingOrZero(t *testing.T) {
	ctx := context.Background()
	group := conloomapi.DefaultGroupPolicy()
	group.Reply.MaxParts = 2
	private := privateDefaults()
	private.Reply.MaxParts = 4
	s := NewService(newTestStore(t), group).WithPrivateDefaults(private)

	body := func(maxParts string) string {
		return `{"policy":` + replyJSON(t, conloomapi.DefaultGroupPolicy(), maxParts) + `,"reason":"test"}`
	}
	for _, tc := range []struct {
		key, maxParts string
		want          int
	}{
		{testPrivateKey, "", 4},
		{testConvKey, "", 2},
		{testPrivateKey, "0", 0},
		{testConvKey, "10", 10},
	} {
		pv, err := s.Update(ctx, tc.key, decodePut(t, body(tc.maxParts)), "29999")
		if err != nil || pv.Policy.Reply.MaxParts != tc.want {
			t.Fatalf("Update %s max_parts %q = %+v, %v; want %d", tc.key, tc.maxParts, pv.Policy.Reply, err, tc.want)
		}
		cur, err := s.Current(ctx, tc.key)
		if err != nil || cur.Policy.Reply.MaxParts != tc.want {
			t.Fatalf("Current %s after max_parts %q = %+v, %v; want %d", tc.key, tc.maxParts, cur.Policy.Reply, err, tc.want)
		}
	}

	for _, n := range []string{"-1", "11"} {
		if _, err := s.Update(ctx, testConvKey, decodePut(t, body(n)), "29999"); err == nil {
			t.Fatalf("Update max_parts %s stored", n)
		}
	}
}
