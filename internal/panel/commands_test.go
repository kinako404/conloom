package panel

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type commandsResp struct {
	Prefix   string `json:"prefix"`
	Commands []struct {
		Name        string   `json:"name"`
		Aliases     []string `json:"aliases"`
		Usage       string   `json:"usage"`
		Description string   `json:"description"`
		Args        bool     `json:"args"`
	} `json:"commands"`
}

func (r commandsResp) names() []string {
	out := []string{}
	for _, c := range r.Commands {
		out = append(out, c.Name)
	}
	return out
}

// A panel chat's command menu lists the commands of a private chat, the
// admins' too since its user counts as an admin, with the policy's prefix.
func TestCommands_PanelChat(t *testing.T) {
	h := newDataHarness(t)
	chat := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	path := convPath(chat.Key, "commands")

	raw := h.getJSON(path, nil)
	for _, key := range []string{`"prefix":"/"`, `"name":"help","aliases":[],"usage":"/help","description":"帮助","args":false`,
		`"name":"记住","aliases":["remember"],"usage":"/记住 \u003c内容\u003e","description":"让我记住一件事","args":true`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("GET commands = %s, want it to contain %s", raw, key)
		}
	}
	var got commandsResp
	h.getJSON(path, &got)
	want := []string{"help", "stop", "persona", "主动", "好感度", "记忆", "记忆 摘要", "记住", "忘记我",
		"pause", "resume", "persona list", "persona use", "好感度 设", "记忆 整理"}
	if !slices.Equal(got.names(), want) {
		t.Errorf("commands = %q, want %q", got.names(), want)
	}
	if !slices.Equal(got.names(), menuNames(core.MenuCommands("/", conloomapi.ConvPrivate, true))) {
		t.Errorf("commands = %q, want core.MenuCommands of a private chat with an admin", got.names())
	}

	p := conloomapi.DefaultGroupPolicy()
	p.Reply.CommandPrefix = "!"
	if _, err := h.policies.Update(context.Background(), chat.Key, conloomapi.PolicyPut{Policy: p}, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	h.getJSON(path, &got)
	if got.Prefix != "!" || len(got.Commands) != len(want) || got.Commands[7].Usage != "!记住 <内容>" {
		t.Errorf("with prefix ! = %+v", got)
	}

	p.Reply.CommandPrefix = ""
	if _, err := h.policies.Update(context.Background(), chat.Key, conloomapi.PolicyPut{Policy: p}, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if raw := strings.TrimSpace(string(h.getJSON(path, nil))); raw != `{"prefix":"","commands":[]}` {
		t.Errorf("without a prefix = %s, want no commands", raw)
	}
}

func menuNames(cmds []core.MenuCommand) []string {
	out := []string{}
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	return out
}

// The chat pages send only in panel chats: other conversations are refused
// as POST messages refuses them, a missing one is 404, and the list needs a
// session.
func TestCommands_RefusedAndAuth(t *testing.T) {
	h := newDataHarness(t)
	for _, conv := range []store.ConversationRecord{
		h.seedConv("qq", conloomapi.ConvGroup, "10001"), h.seedConv("qq", conloomapi.ConvPrivate, "20001"),
	} {
		body := errorBody(t, h.get(convPath(conv.Key, "commands")), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if body.Message != "not a panel chat" {
			t.Errorf("%s: message %q", conv.Key, body.Message)
		}
	}
	h.getErr(convPath("qq-main:panel:private:missing", "commands"), http.StatusNotFound, conloomapi.CodeNotFound)
	chat := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	errorBody(t, h.do(http.MethodGet, convPath(chat.Key, "commands"), ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
}
