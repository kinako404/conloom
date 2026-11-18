package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func newSetScene(st store.Store, policies *policy.Service, doc conloomapi.PersonaDoc) skills.Tool {
	return NewSetScene(st, policies, fakePersonas{doc: doc}, review.New(nil, review.Options{}))
}

func sceneOf(t *testing.T, st store.Store, convKey string) (store.ConversationScene, bool) {
	t.Helper()
	s, err := st.GetScene(context.Background(), convKey)
	if errors.Is(err, store.ErrNotFound) {
		return store.ConversationScene{}, false
	}
	if err != nil {
		t.Fatalf("GetScene: %v", err)
	}
	return s, true
}

// checkScene checks that convKey's scene is mode at place.
func checkScene(t *testing.T, st store.Store, convKey, mode, place string) store.ConversationScene {
	t.Helper()
	s, ok := sceneOf(t, st, convKey)
	if !ok || s.Mode != mode || s.Place != place {
		t.Errorf("scene = %+v (found %v), want %s at %q", s, ok, mode, place)
	}
	return s
}

func TestSetScene_SwitchesAndRecordsPrev(t *testing.T) {
	st := newStore(t)
	tool := newSetScene(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	checkSpec(t, tool.Spec(), "set_scene", "mode")
	if !strings.Contains(string(tool.Spec().Parameters), `"place"`) {
		t.Errorf("parameters = %s lack place", tool.Spec().Parameters)
	}
	inv := invocation(privateKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{"mode":"in_person","place":"食堂"}`), "已记下：面对面，在食堂。"; got != want {
		t.Errorf("set_scene in_person = %q, want %q", got, want)
	}
	s := checkScene(t, st, privateKey, conloomapi.SceneInPerson, "食堂")
	if s.Origin() != privateKey || !s.SinceAt.Equal(testNow) || !s.TouchedAt.Equal(testNow) ||
		s.PrevMode != conloomapi.SceneOnline || s.PrevPlace != "" {
		t.Errorf("scene = %+v, want set in %s at %v after online", s, privateKey, testNow)
	}

	inv.Now = testNow.Add(10 * time.Minute)
	if got, want := call(t, tool, inv, `{"mode":"online","place":"食堂"}`), "已记下：线上。"; got != want {
		t.Errorf("set_scene online = %q, want %q", got, want)
	}
	s = checkScene(t, st, privateKey, conloomapi.SceneOnline, "")
	if s.PrevMode != conloomapi.SceneInPerson || s.PrevPlace != "食堂" || !s.SinceAt.Equal(inv.Now) ||
		s.SwitchTaskID != inv.TaskID {
		t.Errorf("scene = %+v, want online since %v after in person at 食堂, switched by %s", s, inv.Now, inv.TaskID)
	}

	inv.Now = testNow.Add(20 * time.Minute)
	if got, want := call(t, tool, inv, `{"mode":"in_person"}`), "已记下：面对面。"; got != want {
		t.Errorf("set_scene in_person without place = %q, want %q", got, want)
	}
	s = checkScene(t, st, privateKey, conloomapi.SceneInPerson, "")
	if s.PrevMode != conloomapi.SceneOnline || s.PrevPlace != "" {
		t.Errorf("scene = %+v, want prev online", s)
	}
}

func TestSetScene_SameSceneIsNoop(t *testing.T) {
	st := newStore(t)
	tool := newSetScene(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	call(t, tool, inv, `{"mode":"in_person","place":"食堂"}`)
	inv.Now = testNow.Add(15 * time.Minute)
	if got, want := call(t, tool, inv, `{"mode":"in_person","place":" 食堂 "}`), "场景没有变化。"; got != want {
		t.Errorf("repeated set_scene = %q, want %q", got, want)
	}
	s := checkScene(t, st, privateKey, conloomapi.SceneInPerson, "食堂")
	if !s.SinceAt.Equal(testNow) || !s.TouchedAt.Equal(inv.Now) {
		t.Errorf("scene = %+v, want since %v touched %v", s, testNow, inv.Now)
	}

	fresh := invocation(store.ConversationKey("bot_test", "qq", conloomapi.ConvPrivate, "private_29999"), "29999", "小红")
	if got, want := call(t, tool, fresh, `{"mode":"online","place":"图书馆"}`), "场景没有变化。"; got != want {
		t.Errorf("set_scene online without a scene = %q, want %q", got, want)
	}
	if s, ok := sceneOf(t, st, fresh.ConversationKey); ok {
		t.Errorf("online without a scene stored %+v", s)
	}
}

func TestSetScene_BadMode(t *testing.T) {
	st := newStore(t)
	tool := newSetScene(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, args := range []string{`{"mode":"offline"}`, `{"mode":""}`, `{"mode":1}`, `{"place":"食堂"}`, `{"mode":null}`, `[]`} {
		if got, want := call(t, tool, inv, args), "mode 只能是 online（线上）或 in_person（面对面）。"; got != want {
			t.Errorf("set_scene(%s) = %q, want %q", args, got, want)
		}
	}
	if s, ok := sceneOf(t, st, privateKey); ok {
		t.Errorf("a bad mode stored %+v", s)
	}
}

func TestSetScene_PlaceCleanedAndBounded(t *testing.T) {
	st := newStore(t)
	policies := newPolicies(st, true, true)
	tool := newSetScene(st, policies, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, c := range []struct{ place, want string }{
		{`【食堂】`, "食堂"},
		{`食\n堂`, "食 堂"},
		{"食\u200b堂", "食堂"},
		{strings.Repeat("字", conloomapi.ScenePlaceMaxRunes), strings.Repeat("字", conloomapi.ScenePlaceMaxRunes)},
	} {
		call(t, tool, inv, `{"mode":"online"}`)
		got := call(t, tool, inv, `{"mode":"in_person","place":"`+c.place+`"}`)
		if want := "已记下：面对面，在" + c.want + "。"; got != want {
			t.Errorf("set_scene(%q) = %q, want %q", c.place, got, want)
		}
		checkScene(t, st, privateKey, conloomapi.SceneInPerson, c.want)
	}

	call(t, tool, inv, `{"mode":"online"}`)
	if got, want := call(t, tool, inv, `{"mode":"in_person","place":"`+strings.Repeat("字", conloomapi.ScenePlaceMaxRunes+1)+`"}`),
		"地点（place）超过 16 字，写短一点。"; got != want {
		t.Errorf("set_scene with a long place = %q, want %q", got, want)
	}
	checkScene(t, st, privateKey, conloomapi.SceneOnline, "")

	if got, want := call(t, tool, inv, `{"mode":"in_person","place":"   "}`), "已记下：面对面。"; got != want {
		t.Errorf("set_scene with a blank place = %q, want %q", got, want)
	}
	checkScene(t, st, privateKey, conloomapi.SceneInPerson, "")

	word := protectedWord(t)
	fresh := invocation(store.ConversationKey("bot_test", "qq", conloomapi.ConvPrivate, "private_29999"), "29999", "小红")
	if got := call(t, tool, fresh, `{"mode":"in_person","place":"`+word+`"}`); got != "这个地点不能用。" {
		t.Errorf("protected set_scene of a lexicon word = %q, want it refused", got)
	}
	if s, ok := sceneOf(t, st, fresh.ConversationKey); ok {
		t.Errorf("a refused place stored %+v", s)
	}

	adult := conloomapi.DefaultPersona()
	adult.Adult = true
	open := newSetScene(st, policies, adult)
	if got, want := call(t, open, fresh, `{"mode":"in_person","place":"`+word+`"}`), "已记下：面对面，在"+word+"。"; got != want {
		t.Errorf("open set_scene = %q, want %q (the protected lexicon does not apply)", got, want)
	}

	pv, err := policies.Current(context.Background(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	for _, tl := range []skills.Tool{tool, open} {
		if got := call(t, tl, inv, `{"mode":"in_person","place":"坏词店"}`); got != "这个地点不能用。" {
			t.Errorf("set_scene of a blocked word = %q, want it refused", got)
		}
	}
	checkScene(t, st, privateKey, conloomapi.SceneInPerson, "")
	if got, want := call(t, tool, inv, `{"mode":"online","place":"坏词店"}`), "已记下：线上。"; got != want {
		t.Errorf("set_scene online with a blocked place = %q, want %q (the place is dropped)", got, want)
	}

	p.Review.Action = review.VerdictSanitize
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	if got, want := call(t, tool, inv, `{"mode":"in_person","place":"坏词店"}`), "已记下：面对面，在**店。"; got != want {
		t.Errorf("set_scene of a sanitized word = %q, want %q", got, want)
	}
	checkScene(t, st, privateKey, conloomapi.SceneInPerson, "**店")
}

func TestSetScene_GroupRefused(t *testing.T) {
	st := newStore(t)
	tool := newSetScene(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	for _, args := range []string{`{"mode":"in_person","place":"食堂"}`, `{"mode":"online"}`} {
		if got := call(t, tool, invocation(groupKey, "20001", "小明"), args); got != "只能在私聊里用。" {
			t.Errorf("set_scene(%s) in a group = %q", args, got)
		}
	}
	if _, err := st.GetScene(context.Background(), groupKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetScene of the group = %v, want ErrNotFound", err)
	}
}

func TestSetScene_PanelChatsShareTheScope(t *testing.T) {
	st := newStore(t)
	a := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	scope := memory.ScopeKeyOf(a)
	if scope != store.PanelScopeKey("bot_test") {
		t.Fatalf("scope = %s, want the bot's panel scope", scope)
	}
	tool := newSetScene(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inA, inB := invocation(a, "20001", "小明"), invocation(b, "20001", "小明")

	call(t, tool, inA, `{"mode":"in_person","place":"图书馆"}`)
	if s := checkScene(t, st, scope, conloomapi.SceneInPerson, "图书馆"); s.Origin() != a {
		t.Errorf("scope scene origin = %s, want %s", s.Origin(), a)
	}
	inB.Now = testNow.Add(5 * time.Minute)
	if got, want := call(t, tool, inB, `{"mode":"in_person","place":"食堂"}`), "已记下：面对面，在食堂。"; got != want {
		t.Errorf("set_scene in B = %q, want %q", got, want)
	}
	if s := checkScene(t, st, scope, conloomapi.SceneInPerson, "食堂"); s.Origin() != b || s.PrevPlace != "图书馆" {
		t.Errorf("scope scene = %+v, want set in %s after 图书馆", s, b)
	}
	for _, key := range []string{a, b} {
		if s, ok := sceneOf(t, st, key); ok {
			t.Errorf("scene stored under the chat %s: %+v", key, s)
		}
	}
}
