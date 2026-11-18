package web

import (
	"regexp"
	"strings"
	"testing"
)

// The chat profile card loads the conversation, its persona, memory and, in a
// private chat only, its hidden state, each source rendering its own section
// so one that fails shows an error there only; it is a modal dialog built
// from text nodes that polls the hidden state every 5 s while open and ends
// its timer and listeners on close.
func TestProfile_Card(t *testing.T) {
	all := assets(t, ".js")
	js, ok := all["views/profile.js"]
	if !ok {
		t.Fatal("views/profile.js is missing")
	}
	for _, want := range []string{
		"export function openProfile({ key, bot, signal, opener })",
		"get(convPath(key), null, ctrl.signal)",
		"convPath(key, 'persona')", "convPath(key, 'memory')", "convPath(key, 'autonomy')",
		"Promise.allSettled(",
		"role: 'dialog'", "'aria-modal': 'true'",
		"'状态'", "'关系'", "'资料'", "'最近变化'", "'消息'", "'技能'",
		"'这个 bot 没有启用 buff 技能，没有状态条。'",
		"sceneLabel(", "`正在：${", "（还剩 ${",
		"title: '在会话详情里编辑'", "title: '关闭'", "href: href('conversations', key)",
		"POLL_MS = 5000", "setTimeout(poll, POLL_MS)", "clearTimeout(",
		"listen(document, 'keydown'", "e.key === 'Escape'", "e.target === backdrop",
		"document.body.appendChild(backdrop)", "backdrop.remove()", "ctrl.abort()",
		"opener.focus()", "errorBox(", "h('details'", "skills.join('、')",
		"class: 'tg-profile-backdrop'", "class: 'tg-profile'", "'tg-profile-head'",
		"'tg-profile-avatar'", "'tg-profile-name'", "'tg-profile-status'",
		"'tg-profile-section'", "'tg-profile-row'", "'tg-profile-label'",
		"'tg-profile-bar'", "class: 'fill'", "'tg-profile-list'",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/profile.js lacks %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "setInterval(", "document.addEventListener(", "window.addEventListener(", "{ signal })"} {
		if strings.Contains(js, bad) {
			t.Errorf("views/profile.js uses %q", bad)
		}
	}
	// A group has no hidden state: the autonomy request and its polling are
	// behind the private check.
	private := strings.Index(js, "type === 'private'")
	request := strings.Index(js, "convPath(key, 'autonomy')")
	if private < 0 || request < 0 || private > request {
		t.Errorf("views/profile.js must check type === 'private' before requesting convPath(key, 'autonomy') (%d, %d)", private, request)
	}
	// Each section renders from its own settled result.
	if n := len(regexp.MustCompile(`status === 'rejected'|\.status !== 'fulfilled'`).FindAllString(js, -1)); n < 3 {
		t.Errorf("views/profile.js checks %d settled results for rejection, want each section's source checked", n)
	}
	// The page's signal closes the card too.
	if !strings.Contains(js, "listen(signal, 'abort', close, ctrl.signal)") {
		t.Error("views/profile.js does not close the card when the page's signal fires")
	}
}
