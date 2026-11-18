package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// policyWithMaxParts returns p as JSON without reply.max_parts, or with it set
// to maxParts when that is not empty.
func policyWithMaxParts(t *testing.T, p conloomapi.GroupPolicy, maxParts string) json.RawMessage {
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
	return raw
}

func TestPutPolicy_MaxPartsMissingOrZero(t *testing.T) {
	h := newWriteHarness(t, nil)
	h.setDefaults(policy.KindGroup, policyWith(func(p *conloomapi.GroupPolicy) { p.Reply.MaxParts = 3 }))
	path := convPath(writeGroup.Key, "policy")
	for _, tc := range []struct {
		maxParts string
		want     int
	}{
		{"", 3},
		{"0", 0},
		{"7", 7},
	} {
		body := map[string]any{"policy": policyWithMaxParts(t, conloomapi.DefaultGroupPolicy(), tc.maxParts), "reason": "test"}
		got := decodeCurrent[policyVersionResp](t, h.write(http.MethodPut, path, body), http.StatusOK)
		if got.Policy.Reply.MaxParts != tc.want {
			t.Errorf("PUT max_parts %q = %d, want %d", tc.maxParts, got.Policy.Reply.MaxParts, tc.want)
		}
		if cur := h.currentPolicy(); cur.Policy.Reply.MaxParts != tc.want {
			t.Errorf("Current after max_parts %q = %d, want %d", tc.maxParts, cur.Policy.Reply.MaxParts, tc.want)
		}
	}
	for _, n := range []string{"-1", "11"} {
		body := map[string]any{"policy": policyWithMaxParts(t, conloomapi.DefaultGroupPolicy(), n)}
		eb := errorBody(t, h.write(http.MethodPut, path, body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(eb.Message, "reply.max_parts must be in [0, 10], got "+n) {
			t.Errorf("max_parts %s message = %q", n, eb.Message)
		}
	}
}

func TestRestorePolicy_VersionWithoutMaxParts(t *testing.T) {
	h := newWriteHarness(t, nil)
	h.setDefaults(policy.KindGroup, policyWith(func(p *conloomapi.GroupPolicy) { p.Reply.MaxParts = 3 }))
	old := policyWith(func(p *conloomapi.GroupPolicy) { p.Context.MaxRecentMessages = 30 })
	var legacy conloomapi.GroupPolicy
	if err := json.Unmarshal(policyWithMaxParts(t, old, ""), &legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.AppendPolicyVersion(context.Background(), store.PolicyVersion{
		ConversationKey: writeGroup.Key, Policy: legacy, UpdatedBy: "system",
	}); err != nil {
		t.Fatal(err)
	}
	if w := h.write(http.MethodPost, convPath(writeGroup.Key, "pause"), nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}

	var pol policyResp
	raw := h.getJSON(convPath(writeGroup.Key, "policy"), &pol)
	if pol.Current.Policy.Reply.MaxParts != 3 || len(pol.Versions) != 2 || pol.Versions[0].Policy.Reply.MaxParts != 3 {
		t.Errorf("GET policy = %s, want max_parts 3 filled and stored by pause", raw)
	}
	if strings.Count(string(raw), `"max_parts"`) != 2 || strings.Contains(string(raw), "-9223372036854775808") {
		t.Errorf("GET policy = %s, want max_parts in the current policy and the paused version only", raw)
	}

	got := decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "policy", "restore"),
		map[string]any{"version": 1}), http.StatusOK)
	if got.Version != 3 || got.Policy.Reply.MaxParts != 3 || got.Policy.Context.MaxRecentMessages != 30 {
		t.Errorf("restore v1 = %+v, want v3 with max_parts 3 and max_recent_messages 30", got)
	}
}

func TestSettingsPolicies_PutMaxPartsMissingOrZero(t *testing.T) {
	h := newWriteHarness(t, nil)
	resp := h.save(http.MethodPut, settingsPath+"/private",
		map[string]any{"policy": map[string]any{"reply": map[string]any{"quote": "never"}}})
	if resp.Current.Policy.Reply.MaxParts != conloomapi.DefaultPrivatePolicy().Reply.MaxParts || resp.Current.Policy.Reply.Quote != "never" {
		t.Errorf("PUT without max_parts = %+v, want the built-in max_parts", resp.Current.Policy.Reply)
	}
	resp = h.save(http.MethodPut, settingsPath+"/private",
		map[string]any{"policy": map[string]any{"reply": map[string]any{"max_parts": 0}}})
	if resp.Current.Policy.Reply.MaxParts != 0 {
		t.Errorf("PUT max_parts 0 = %d, want 0", resp.Current.Policy.Reply.MaxParts)
	}
	if d := h.policies.Defaults(policy.KindPrivate); d.Policy.Reply.MaxParts != 0 {
		t.Errorf("stored private default max_parts = %d, want 0", d.Policy.Reply.MaxParts)
	}
	eb := errorBody(t, h.write(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"reply": map[string]any{"max_parts": 11}}}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if !strings.Contains(eb.Message, "reply.max_parts must be in [0, 10], got 11") {
		t.Errorf("max_parts 11 message = %q", eb.Message)
	}
}
