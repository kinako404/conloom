package web

import (
	"regexp"
	"strconv"
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
		"'tg-profile-section'", "'tg-profile-item'", "'tg-profile-value'", "'tg-profile-label'", "'tg-profile-body'",
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

// Each row of the card is one inset item holding its value and, under it,
// its muted label; a section's hints, loading, empty notes and errors sit in
// its body with the same inset; a band separates the head from the first
// section and each section from the next.
func TestProfile_Layout(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/profile.js"]
	if !regexp.MustCompile(`h\('div', \{ class: 'tg-profile-item' \},\s*h\('div', \{ class: 'tg-profile-value' \}, value\),\s*h\('div', \{ class: 'tg-profile-label' \}, text\)\)`).MatchString(js) {
		t.Error("views/profile.js: item does not wrap its value and label in one .tg-profile-item")
	}
	if strings.Contains(js, "'tg-profile-row'") {
		t.Error("views/profile.js still uses .tg-profile-row")
	}
	if n := strings.Count(js, "h('div', { class: 'tg-profile-body' }, loading())"); n != 3 {
		t.Errorf("views/profile.js has %d section bodies with class tg-profile-body, want 3", n)
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(files["style.css"], "")
	for _, c := range []struct{ name, re string }{
		{"an inset item", `\.chat-app \.tg-profile-item\s*\{[^}]*padding:\s*\d+px 16px;`},
		{"inset hints, loading and empty notes", `\.chat-app \.tg-profile-body > p\s*\{[^}]*margin:\s*\d+px 16px;`},
		{"an inset error", `\.chat-app \.tg-profile \.error\s*\{[^}]*margin:\s*\d+px 16px;`},
		{"a band over every section and the list", `\.chat-app \.tg-profile-section,\s*\.chat-app \.tg-profile-list\s*\{[^}]*border-top:\s*8px solid`},
	} {
		if !regexp.MustCompile(c.re).MatchString(css) {
			t.Errorf("style.css lacks %s (%s)", c.name, c.re)
		}
	}
	for _, bad := range []string{".tg-profile-head + *", ".tg-profile-row", ".tg-profile-section > p"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css still has %q", bad)
		}
	}
	if regexp.MustCompile(`\.tg-profile-label\s*\{[^}]*order:`).MatchString(css) {
		t.Error("style.css reorders .tg-profile-label")
	}
}

// 关系 and 资料 fold under their headings, open at first, with the accent
// triangle of the folding items; 状态 keeps a plain heading. Polling remounts
// only the 状态 body and the status line, so a folded section stays folded.
func TestProfile_Fold(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/profile.js"]
	for _, want := range []string{
		"h('details', { class: 'tg-profile-fold', open: true }, h('summary', null, heading), body)",
		"const heading = h('h3', null, title);",
		"section('资料', profileEl, true)",
		"section('状态', stateEl), section('关系', relationEl, true)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/profile.js lacks %q", want)
		}
	}
	poll := regexp.MustCompile(`(?s)const showState = .*?\n  \};`).FindString(js)
	if poll == "" {
		t.Fatal("views/profile.js lacks showState")
	}
	for _, bad := range []string{"privateEls", "section(", "relationEl", "profileEl"} {
		if strings.Contains(poll, bad) {
			t.Errorf("views/profile.js: showState touches %q, so polling would reset the folded sections", bad)
		}
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(files["style.css"], "")
	for _, c := range []struct{ name, re string }{
		{"a fold heading styled as the plain heading", `\.chat-app \.tg-profile-section > h3,\s*\.chat-app \.tg-profile-section > \.tg-profile-fold > summary\s*\{[^}]*margin:\s*6px 16px 2px;[^}]*color:\s*var\(--accent\);[^}]*font-size:\s*14px;[^}]*font-weight:\s*600;`},
		{"an accent triangle", `\.chat-app \.tg-profile-fold > summary::marker\s*\{\s*color:\s*var\(--accent\);`},
		{"an inline heading in the toggle", `\.chat-app \.tg-profile-fold > summary > h3\s*\{[^}]*display:\s*inline;[^}]*margin:\s*0;`},
		{"a focus ring on the toggle", `\.chat-app \.tg-profile summary:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--accent\);`},
	} {
		if !regexp.MustCompile(c.re).MatchString(css) {
			t.Errorf("style.css lacks %s (%s)", c.name, c.re)
		}
	}
}

// Each section renders as soon as its own sources settle; a failed refresh
// keeps the error already shown when the message is the same.
func TestProfile_Sections(t *testing.T) {
	js := assets(t, ".js")["views/profile.js"]
	if strings.Contains(js, "Promise.allSettled([convP, personaP, memoryP, autonomyP, skillsP])") {
		t.Error("views/profile.js waits for every source before rendering any section")
	}
	for _, want := range []string{
		"done([convP], ", "done([convP, personaP], ", "done([convP, memoryP], ",
		"done([convP, autonomyP, skillsP], ", "done([skillsP], ",
		"if (text === stateError) return;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/profile.js lacks %q", want)
		}
	}
}

// Closing fades the card out briefly, except under reduced motion, when the
// page goes away or when ✎ navigates, which remove it at once; the focus goes
// back to the opener only while it is still on the page; only a plain left
// click on ✎ closes the card.
func TestProfile_Closing(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/profile.js"]
	for _, want := range []string{
		"matchMedia('(prefers-reduced-motion: reduce)').matches",
		"signal && signal.aborted", "backdrop.classList.add('closing')",
		"backdrop.addEventListener('animationend'", "setTimeout(() => backdrop.remove(), FADE_MS)",
		"opener && opener.isConnected",
		"e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/profile.js lacks %q", want)
		}
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(files["style.css"], "")
	m := regexp.MustCompile(`\.chat-app \.tg-profile-backdrop\.closing\s*\{[^}]*animation:\s*tg-fade-out (\d+)ms ease-out forwards;`).FindStringSubmatch(css)
	if m == nil {
		t.Fatal("style.css: a closing card does not fade out")
	}
	if ms, _ := strconv.Atoi(m[1]); ms > 150 {
		t.Errorf("style.css: the card fades out in %dms, want at most 150ms", ms)
	}
	if !regexp.MustCompile(`\.chat-app \.tg-profile-backdrop\.closing\s*\{[^}]*pointer-events:\s*none;`).MatchString(css) {
		t.Error("style.css: a closing card still takes clicks")
	}
	if !regexp.MustCompile(`@keyframes tg-fade-out\s*\{\s*to\s*\{\s*opacity:\s*0;`).MatchString(css) {
		t.Error("style.css lacks @keyframes tg-fade-out")
	}
	if !regexp.MustCompile(`const FADE_MS = (\d+);`).MatchString(js) {
		t.Error("views/profile.js lacks FADE_MS")
	}
}
