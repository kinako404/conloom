package gateway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestPolicy_PutMaxPartsMissingOrZero(t *testing.T) {
	s := newTestServer(t)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	const path = "/api/v1/conversations/group_10001/policy"
	var cur conloomapi.PolicyState
	decode(t, s.do(t, http.MethodGet, path, adminToken, nil, nil).body, &cur)
	if cur.Policy.Reply.MaxParts != 1 {
		t.Fatalf("default max_parts = %d, want 1", cur.Policy.Reply.MaxParts)
	}
	cur.Policy.Reply.MaxParts = 4
	body := func(maxParts string) []byte {
		raw, err := json.Marshal(cur.Policy)
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
		raw, err = json.Marshal(map[string]any{"policy": doc, "reason": "test"})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, tc := range []struct {
		maxParts string
		want     int
	}{
		{"0", 0},
		{"", 1},
		{"10", 10},
	} {
		r := s.do(t, http.MethodPut, path, adminToken, body(tc.maxParts), nil)
		var put conloomapi.PolicyState
		decode(t, r.body, &put)
		if r.status != http.StatusOK || put.Policy.Reply.MaxParts != tc.want {
			t.Fatalf("PUT max_parts %q: status %d, body %s; want max_parts %d", tc.maxParts, r.status, r.body, tc.want)
		}
	}
	e := expectError(t, s.do(t, http.MethodPut, path, adminToken, body("-1"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if !strings.Contains(e.Message, "reply.max_parts must be in [0, 10], got -1") {
		t.Fatalf("max_parts -1 message = %q", e.Message)
	}
}
