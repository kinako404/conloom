package gateway_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestGatewayAutonomy_Scene(t *testing.T) {
	ms := newMemServer(t, false)
	ms.seedAutonomyConvs(t)
	scene := autonomyPath + "/scene"
	put := conloomapi.AutonomyScenePut{Mode: conloomapi.SceneInPerson, Place: "食堂"}

	expectError(t, ms.do(t, http.MethodPut, scene, "", put, nil), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	expectError(t, ms.do(t, http.MethodPut, scene, memoryReaderToken, put, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, ms.do(t, http.MethodPut, scene, adminToken, put, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, ms.do(t, http.MethodPut, scene, "test-memory-other-token", put, nil), http.StatusNotFound, conloomapi.CodeNotFound)

	r := ms.do(t, http.MethodPut, scene, memoryToken, put, nil)
	if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
		t.Fatalf("PUT scene: status %d, body %s", r.status, r.body)
	}
	sc, err := ms.st.GetScene(context.Background(), autonomyPrivateKey)
	if err != nil || sc.Mode != conloomapi.SceneInPerson || sc.Place != "食堂" || sc.Origin() != autonomyPrivateKey {
		t.Fatalf("stored scene = %+v, %v; want in person at 食堂", sc, err)
	}
	raw := ms.do(t, http.MethodGet, autonomyPath, memoryReaderToken, nil, nil)
	if raw.status != http.StatusOK || !strings.Contains(string(raw.body), `"scene":{"enabled":`) ||
		!strings.Contains(string(raw.body), `"mode":"in_person","place":"食堂"`) {
		t.Errorf("GET autonomy = %s, want the scene", raw.body)
	}
	if st := ms.autonomyState(t, memoryReaderToken); st.Scene.Mode != conloomapi.SceneInPerson || st.Scene.SinceAt.IsZero() {
		t.Errorf("scene = %+v, want in person with a start", st.Scene)
	}
	audits := ms.autonomyAudits(t, "autonomy.scene.set")
	if len(audits) != 1 || audits[0].Actor != "memory-main" || audits[0].Target != autonomyPrivateKey || audits[0].BotInstanceID != "bot_main" ||
		audits[0].Detail != `{"mode":"in_person","place":"食堂","source":"api","changed":true}` {
		t.Errorf("autonomy.scene.set audits = %+v", audits)
	}

	expectError(t, ms.do(t, http.MethodPut, "/api/v1/conversations/group_10001/autonomy/scene", memoryToken, put, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	long := conloomapi.AutonomyScenePut{Mode: conloomapi.SceneInPerson, Place: strings.Repeat("食", conloomapi.ScenePlaceMaxRunes+1)}
	if e := expectError(t, ms.do(t, http.MethodPut, scene, memoryToken, long, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "16") ||
		strings.Contains(e.Message, "core:") {
		t.Errorf("long place: message %q", e.Message)
	}
	if e := expectError(t, ms.do(t, http.MethodPut, scene, memoryToken, map[string]string{"mode": "offline"}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "offline") {
		t.Errorf("unknown mode: message %q", e.Message)
	}
	expectError(t, ms.do(t, http.MethodPut, scene, memoryToken, "{", nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if n := len(ms.autonomyAudits(t, "autonomy.scene.set")); n != 1 {
		t.Errorf("refused PUTs wrote audits: %d in all, want 1", n)
	}
	if sc, err := ms.st.GetScene(context.Background(), autonomyPrivateKey); err != nil || sc.Place != "食堂" {
		t.Errorf("stored scene after refused PUTs = %+v, %v", sc, err)
	}
}
