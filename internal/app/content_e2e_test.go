package app_test

import (
	"encoding/base64"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	contentTemplate = "fake_persona"
	contentRules    = "（假的内容规则：这一行只在开放模式出现）"
)

var (
	contentPrivate    = mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}
	contentPrivateKey = store.ConversationKey(botID, "qq", conloomapi.ConvPrivate, "private_"+admin.PlatformID)
	contentGroupKey   = store.ConversationKey(botID, "qq", conloomapi.ConvGroup, "group_"+group.PlatformID)
)

// contentExplicitWord is the first entry of the built-in protected lexicon,
// decoded at run time so that no plaintext entry appears in the source.
func contentExplicitWord(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../review/protected_words.b64")
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	w, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(w) == 0 {
		t.Fatalf("decode the first lexicon entry: %v", err)
	}
	return string(w)
}

// contentPersona is a fake persona with content rules, marked adult or minor.
func contentPersona(adult, minor bool) conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName = "测试人设"
	p.CharacterName = "阿青"
	p.Adult, p.Minor = adult, minor
	p.ContentRules = contentRules
	return p
}

// contentEnv starts an app whose bot uses persona, with a fake model that
// always answers reply, replies of up to 3 parts and a short private_wait, and
// logs into its panel.
func contentEnv(t *testing.T, persona conloomapi.PersonaDoc, reply string) (*env, *fakeopenai.Server, *panelClient) {
	t.Helper()
	fake := fakeopenai.New(t, answer(reply))
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	cfg.PersonaTemplates[contentTemplate] = persona
	cfg.Bots[0].PersonaTemplate = contentTemplate
	cfg.DefaultGroupPolicy.Reply.MaxParts = 3
	private := conloomapi.DefaultGroupPolicy()
	private.Reply.MaxParts = 3
	private.Reply.PrivateWait = conloomapi.Duration{Duration: 20 * time.Millisecond}
	private.Reply.PrivateMaxWait = conloomapi.Duration{Duration: 200 * time.Millisecond}
	cfg.DefaultPrivatePolicy = &private
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	e := newEnv(t, cfg)
	return e, fake, panelLogin(t, e.srv.URL)
}

// contentSystemPrompt is the system message of the model request req.
func contentSystemPrompt(t *testing.T, req map[string]any) string {
	t.Helper()
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("model request has no messages")
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "system" {
		t.Fatalf("first message role = %v, want system", m["role"])
	}
	s, _ := m["content"].(string)
	return s
}

// contentSayPrivate sends text in the admin's private chat and requires a
// reply task.
func contentSayPrivate(e *env, text string) {
	e.t.Helper()
	if resp, _ := e.say(contentPrivate, admin, text, mock.SayOptions{}); resp.Disposition != conloomapi.DispositionReply {
		e.t.Fatalf("private %q: response %+v, want a reply task", text, resp)
	}
}

// requireProtectedBlock waits for the review.block audit event of the
// conversation convKey with the protected_content reason, and requires that
// no audit event carries the blocked word and that nothing is sent.
func requireProtectedBlock(t *testing.T, e *env, p *panelClient, convKey, word string) {
	t.Helper()
	type auditPage struct {
		Events []struct {
			Kind   string `json:"kind"`
			Actor  string `json:"actor"`
			Target string `json:"target"`
			Detail struct {
				TaskID  string   `json:"task_id"`
				Reasons []string `json:"reasons"`
			} `json:"detail"`
		} `json:"events"`
	}
	var raw []byte
	waitUntil(t, "the review.block audit of "+convKey, func() bool {
		var page auditPage
		raw = p.get("/panel/api/audit?kind=review.block&bot="+url.QueryEscape(botID), &page)
		for _, ev := range page.Events {
			if ev.Target == convKey && ev.Actor == "system" && ev.Detail.TaskID != "" &&
				slices.Contains(ev.Detail.Reasons, review.ReasonProtectedContent) {
				return true
			}
		}
		return false
	})
	if strings.Contains(string(raw), word) {
		t.Error("the review.block audit events carry the blocked word")
	}
	if actions := e.drain(300 * time.Millisecond); len(actions) != 0 {
		t.Fatalf("drained %d actions after a protected block, want none", len(actions))
	}
}

func TestE2E_ContentMinorPersonaPrivateChatBlocksExplicitReply(t *testing.T) {
	word := contentExplicitWord(t)
	e, fake, p := contentEnv(t, contentPersona(false, true), "好的，"+word+"。")

	contentSayPrivate(e, "在吗？")
	requireProtectedBlock(t, e, p, contentPrivateKey, word)
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	if sys := contentSystemPrompt(t, reqs[0]); strings.Contains(sys, contentRules) {
		t.Errorf("a minor persona's private system prompt holds its content rules:\n%s", sys)
	}
}

func TestE2E_ContentAdultPersonaOpenOnlyInPrivateChat(t *testing.T) {
	word := contentExplicitWord(t)
	reply := "好的，" + word + "。"
	e, fake, p := contentEnv(t, contentPersona(true, false), reply)

	contentSayPrivate(e, "在吗？")
	a := e.drainOne()
	if a.Content.Text != reply {
		t.Fatalf("private reply has %d runes, want the model reply of %d runes",
			utf8.RuneCountInString(a.Content.Text), utf8.RuneCountInString(reply))
	}
	if a.ConversationID != "private_"+admin.PlatformID {
		t.Errorf("private reply conversation = %q", a.ConversationID)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	if sys := contentSystemPrompt(t, reqs[0]); !strings.Contains(sys, contentRules) {
		t.Errorf("the open private system prompt lacks the content rules:\n%s", sys)
	}

	e.mention(group, alice, "在吗？")
	requireProtectedBlock(t, e, p, contentGroupKey, word)
	reqs = fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(reqs))
	}
	if sys := contentSystemPrompt(t, reqs[1]); strings.Contains(sys, contentRules) {
		t.Errorf("the group system prompt holds the content rules:\n%s", sys)
	}
}

func TestE2E_ContentProtectedBlocksWordSplitAcrossParts(t *testing.T) {
	word := contentExplicitWord(t)
	_, size := utf8.DecodeRuneInString(word)
	e, fake, p := contentEnv(t, conloomapi.DefaultPersona(), "好的"+word[:size]+"\n|||\n"+word[size:]+"。")

	e.mention(group, alice, "在吗？")
	requireProtectedBlock(t, e, p, contentGroupKey, word)
	if n := fake.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
}
