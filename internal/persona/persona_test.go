package persona

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testBot = "bot_test"

var testConvKey = store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10001")

func newTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCorePrompt_InsertsBotName(t *testing.T) {
	got := CorePrompt("织语")
	if !strings.Contains(got, "你是织语") {
		t.Errorf("CorePrompt missing bot name: %q", got)
	}
	if strings.Contains(got, "{bot_name}") {
		t.Errorf("CorePrompt still has placeholder: %q", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("CorePrompt has trailing newline")
	}
}

func TestCorePrompt_ForbidsPretendingToAct(t *testing.T) {
	got := CorePrompt("织语")
	for _, want := range []string{"8. 操作：", "/help", "不要假装已经做了", "不要编造不存在的命令"} {
		if !strings.Contains(got, want) {
			t.Errorf("CorePrompt missing %q: %q", want, got)
		}
	}
}

func TestValidatePersona(t *testing.T) {
	if err := Validate(conloomapi.DefaultPersona()); err != nil {
		t.Errorf("DefaultPersona: %v", err)
	}
	p := conloomapi.DefaultPersona()
	p.Style.Warmth = 1.2
	if err := Validate(p); err == nil || !errors.Is(err, ErrInvalid) {
		t.Errorf("Warmth=1.2: err = %v, want ErrInvalid", err)
	}
	p = conloomapi.DefaultPersona()
	p.Behavior.DefaultReplyLength = "huge"
	if err := Validate(p); err == nil || !strings.Contains(err.Error(), "default_reply_length") {
		t.Errorf("DefaultReplyLength=huge: err = %v", err)
	}
}

// testCharacter is an original, made-up character used only in tests.
func testCharacter() conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName = "灯塔看守"
	p.CharacterName = "苔灯"
	p.Description = "苔灯是海边小镇的灯塔看守，十几岁，做事认真，喜欢收集贝壳。\n她把群友当成镇上的邻居。"
	p.SpeakingStyle = "说话轻快，句子短，偶尔用“呐”结尾。"
	p.Examples = []conloomapi.PersonaExample{
		{User: "今天好累", Reply: "辛苦啦，喝口热水歇一会儿吧。"},
		{User: "你会修电脑吗", Reply: "唔，这个我不太在行呐，你先说说是哪里不对劲？"},
	}
	return p
}

func TestValidatePersona_TextFields(t *testing.T) {
	if err := Validate(testCharacter()); err != nil {
		t.Fatalf("testCharacter: %v", err)
	}
	examples := func(n int) []conloomapi.PersonaExample {
		out := make([]conloomapi.PersonaExample, n)
		for i := range out {
			out[i] = conloomapi.PersonaExample{User: "在吗", Reply: "在呐"}
		}
		return out
	}
	ok := []func(p *conloomapi.PersonaDoc){
		func(p *conloomapi.PersonaDoc) { p.CharacterName = strings.Repeat("名", 20) },
		func(p *conloomapi.PersonaDoc) { p.Description = strings.Repeat("设", 2000) },
		func(p *conloomapi.PersonaDoc) { p.Description = strings.Repeat("设", 2000) + "\n" },
		func(p *conloomapi.PersonaDoc) { p.SpeakingStyle = strings.Repeat("说", 1000) },
		func(p *conloomapi.PersonaDoc) { p.Examples = examples(8) },
		func(p *conloomapi.PersonaDoc) {
			p.Examples = []conloomapi.PersonaExample{{User: strings.Repeat("问", 200), Reply: strings.Repeat("答", 200)}}
		},
		func(p *conloomapi.PersonaDoc) {
			p.CharacterName, p.Description, p.SpeakingStyle, p.Examples = "", "", "", nil
		},
	}
	for i, f := range ok {
		p := testCharacter()
		f(&p)
		if err := Validate(p); err != nil {
			t.Errorf("ok case %d: %v", i, err)
		}
	}
	bad := []struct {
		f    func(p *conloomapi.PersonaDoc)
		want string
	}{
		{func(p *conloomapi.PersonaDoc) { p.CharacterName = strings.Repeat("名", 21) }, "character_name must be at most 20 characters, got 21"},
		{func(p *conloomapi.PersonaDoc) { p.CharacterName = "苔\n灯" }, "character_name must be a single line"},
		{func(p *conloomapi.PersonaDoc) { p.Description = strings.Repeat("设", 2001) }, "description must be at most 2000 characters, got 2001"},
		{func(p *conloomapi.PersonaDoc) { p.SpeakingStyle = strings.Repeat("说", 1001) }, "speaking_style must be at most 1000 characters, got 1001"},
		{func(p *conloomapi.PersonaDoc) { p.Examples = examples(9) }, "examples must have at most 8 items, got 9"},
		{func(p *conloomapi.PersonaDoc) { p.Examples[1].User = " " }, "examples[1].user must not be empty"},
		{func(p *conloomapi.PersonaDoc) { p.Examples[0].Reply = "" }, "examples[0].reply must not be empty"},
		{func(p *conloomapi.PersonaDoc) { p.Examples[0].Reply = strings.Repeat("答", 201) }, "examples[0].reply must be at most 200 characters, got 201"},
		{func(p *conloomapi.PersonaDoc) { p.Examples[1].User = strings.Repeat("问", 201) }, "examples[1].user must be at most 200 characters, got 201"},
	}
	for _, c := range bad {
		p := testCharacter()
		c.f(&p)
		err := Validate(p)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want ErrInvalid containing %q", err, c.want)
		}
	}
}

func TestRender_DeterministicAndVaries(t *testing.T) {
	p := conloomapi.DefaultPersona()
	a, b := Render(p), Render(p)
	if a != b {
		t.Fatalf("Render not deterministic:\n%q\n%q", a, b)
	}
	if first, _, _ := strings.Cut(a, "\n"); !strings.Contains(first, "人格：默认群友人格") {
		t.Errorf("first line = %q", first)
	}
	low, high := p, p
	low.Style.Verbosity = 0.1
	high.Style.Verbosity = 0.9
	if Render(low) == Render(high) {
		t.Errorf("Verbosity 0.1 and 0.9 render the same: %q", Render(low))
	}
	other := p
	other.Behavior.AskFollowupQuestions = "never"
	if Render(other) == a {
		t.Errorf("behavior change not rendered")
	}
}

func TestService_CurrentCreatesV1FromTemplate(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	tmpl := conloomapi.DefaultPersona()
	tmpl.PersonaName = "安静人格"
	tmpl.Style.Humor = 0.1
	svc := NewService(st, map[string]conloomapi.PersonaDoc{"quiet": tmpl})

	pv, err := svc.Current(ctx, testConvKey, "quiet")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if pv.Version != 1 || pv.Reason != "init" || pv.UpdatedBy != "system" || pv.TemplateName != "quiet" ||
		!reflect.DeepEqual(pv.Persona, tmpl) {
		t.Fatalf("Current = %+v", pv)
	}
	again, err := svc.Current(ctx, testConvKey, "quiet")
	if err != nil || again.Version != 1 {
		t.Fatalf("second Current = %+v, %v; want version 1", again, err)
	}

	key2 := store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10002")
	def, err := svc.Current(ctx, key2, "default")
	if err != nil || !reflect.DeepEqual(def.Persona, conloomapi.DefaultPersona()) {
		t.Fatalf("default template Current = %+v, %v", def, err)
	}
	if _, err := svc.Current(ctx, store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10003"), "nope"); err == nil {
		t.Fatal("unknown template: want error")
	}
}

func TestService_ConcurrentCurrentCreatesOneVersion(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Current(ctx, testConvKey, "default"); err != nil {
				t.Errorf("Current: %v", err)
			}
		}()
	}
	wg.Wait()
	if latest, err := st.LatestPersona(ctx, testConvKey); err != nil || latest.Version != 1 {
		t.Fatalf("latest = %+v, %v; want version 1", latest, err)
	}
}

func TestService_UpdateAndRollback(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := &auditRecorder{Store: st}
	svc := NewService(rec, nil)
	v1, err := svc.Current(ctx, testConvKey, "default")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	edited := v1.Persona
	edited.Style.Humor = 0.9
	v2, err := svc.Update(ctx, testConvKey, "default", conloomapi.PersonaPut{Persona: &edited, Reason: "more fun"}, "29999")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if v2.Version != 2 || v2.Persona.Style.Humor != 0.9 || v2.Reason != "more fun" || v2.UpdatedBy != "29999" {
		t.Fatalf("v2 = %+v", v2)
	}

	one := 1
	v3, err := svc.Update(ctx, testConvKey, "default", conloomapi.PersonaPut{RollbackTo: &one}, "29999")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if v3.Version != 3 || !reflect.DeepEqual(v3.Persona, v1.Persona) || v3.Reason != "rollback to v1" {
		t.Fatalf("v3 = %+v", v3)
	}
	cur, err := svc.Current(ctx, testConvKey, "default")
	if err != nil || cur.Version != 3 {
		t.Fatalf("Current after rollback = %+v, %v", cur, err)
	}

	bad := v1.Persona
	bad.Style.Warmth = -0.1
	for name, put := range map[string]conloomapi.PersonaPut{
		"neither": {},
		"both":    {Persona: &edited, RollbackTo: &one},
		"invalid": {Persona: &bad},
	} {
		if _, err := svc.Update(ctx, testConvKey, "default", put, "29999"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	missing := 99
	if _, err := svc.Update(ctx, testConvKey, "default", conloomapi.PersonaPut{RollbackTo: &missing}, "29999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rollback to missing version: err = %v, want ErrNotFound", err)
	}
	if latest, _ := st.LatestPersona(ctx, testConvKey); latest.Version != 3 {
		t.Errorf("failed updates appended versions: latest = %d", latest.Version)
	}
	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("audit events = %+v, want 2", events)
	}
	for _, ev := range events {
		if ev.ID == "" || ev.BotInstanceID != testBot || ev.Kind != "persona.update" || ev.Actor != "29999" ||
			ev.Target != testConvKey || strings.Contains(ev.Detail, "more fun") || ev.Detail == "" {
			t.Errorf("audit event = %+v", ev)
		}
	}
}

func TestService_SystemPromptStablePerVersion(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, nil)

	a, va, err := svc.SystemPrompt(ctx, testConvKey, "default", "织语")
	if err != nil {
		t.Fatalf("SystemPrompt: %v", err)
	}
	b, vb, err := svc.SystemPrompt(ctx, testConvKey, "default", "织语")
	if err != nil {
		t.Fatalf("SystemPrompt: %v", err)
	}
	if a != b || va != 1 || vb != 1 {
		t.Fatalf("same version differs: v%d %q / v%d %q", va, a, vb, b)
	}
	if want := CorePrompt("织语") + "\n\n" + Render(conloomapi.DefaultPersona()); a != want {
		t.Fatalf("prompt = %q, want %q", a, want)
	}

	edited := conloomapi.DefaultPersona()
	edited.Style.Verbosity = 0.9
	if _, err := svc.Update(ctx, testConvKey, "default", conloomapi.PersonaPut{Persona: &edited}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	c, vc, err := svc.SystemPrompt(ctx, testConvKey, "default", "织语")
	if err != nil {
		t.Fatalf("SystemPrompt: %v", err)
	}
	if vc != 2 || c == a {
		t.Fatalf("after update: version %d, prompt unchanged = %v", vc, c == a)
	}
}

type auditRecorder struct {
	store.Store
	mu     sync.Mutex
	events []store.AuditEvent
}

func (r *auditRecorder) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, a)
	r.mu.Unlock()
	return r.Store.InsertAudit(ctx, a)
}

func (r *auditRecorder) all() []store.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}
