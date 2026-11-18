package web

import (
	"regexp"
	"strings"
	"testing"
)

// The bot's avatar and name in the chat's top bar are one button, 查看资料,
// which opens the profile card (views/profile.js); a top bar without a bot
// keeps plain markup. The card is a centered modal over a dimmed backdrop,
// full screen on a narrow screen, which fades and slides up as it opens, on
// the Telegram-style page only, and stands still when reduced motion is
// asked for.
func TestProfile_HeaderEntry(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chat.js"]
	for _, want := range []string{
		"import { openProfile } from './profile.js';",
		"class: 'tg-bar-profile'", "'aria-label': `查看资料：${", "title: '查看资料'",
		"'aria-describedby': 'tg-bar-status'", "statusEl.id = 'tg-bar-status';",
		"openProfile({ key, bot, signal, opener: profile })",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	if !regexp.MustCompile(`h\('button', \{\s*type: 'button', class: 'tg-bar-profile'`).MatchString(js) {
		t.Error("views/chat.js: the profile entry is not a type=button button")
	}
	if !regexp.MustCompile(`const profile = key && bot \?`).MatchString(js) {
		t.Error("views/chat.js: the profile entry is not limited to a bar with a key and a bot")
	}

	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(files["style.css"], "")
	for _, c := range []struct{ name, re string }{
		{"a header button that looks like the plain header", `\.chat-app \.tg-bar-profile\s*\{[^}]*background:\s*none;[^}]*border:\s*none;`},
		{"a visible focus ring on it", `\.chat-app \.tg-bar-profile:focus-visible\s*\{[^}]*(?:outline|box-shadow):`},
		{"a backdrop over the window", `\.chat-app \.tg-profile-backdrop\s*\{[^}]*position:\s*fixed;[^}]*inset:\s*0;`},
		{"a centered card about 420px wide", `\.chat-app \.tg-profile\s*\{[^}]*width:\s*min\(420px,`},
		{"a coloured header band", `\.chat-app \.tg-profile-head\s*\{[^}]*background:`},
		{"rows with a muted label", `\.chat-app \.tg-profile-label\s*\{[^}]*color:\s*var\(--muted\);`},
		{"a progress bar", `\.chat-app \.tg-profile-bar \.fill\s*\{`},
		{"counts on the right of the bottom rows", `\.chat-app \.tg-profile-count\s*\{[^}]*margin-left:\s*auto;`},
		{"collapsed long text", `\.chat-app \.tg-profile summary\s*\{`},
		{"a small inline error", `\.chat-app \.tg-profile \.error\s*\{`},
		{"the backdrop fades in", `\.chat-app \.tg-profile-backdrop\s*\{[^}]*animation:\s*tg-fade-in `},
		{"the card slides up", `\.chat-app \.tg-profile\s*\{[^}]*animation:\s*tg-profile-in `},
	} {
		if !regexp.MustCompile(c.re).MatchString(css) {
			t.Errorf("style.css lacks %s (%s)", c.name, c.re)
		}
	}
	if !regexp.MustCompile(`@keyframes tg-profile-in\s*\{[^}]*translateY\(`).MatchString(css) {
		t.Error("style.css: tg-profile-in does not slide")
	}
	if !regexp.MustCompile(`\.chat-app \.tg-profile\s*\{[^}]*width:\s*100%;[^}]*height:\s*100%;`).MatchString(mediaBlock(t, css, "(max-width: 600px)")) {
		t.Error("style.css @media (max-width: 600px) does not make the card full screen")
	}
	// The card is appended to the body, which app.js gives the class chat-app
	// on this page, so .chat-app rules, and the reduced-motion rule for every
	// .chat-app element, reach it.
	if !strings.Contains(files["app.js"], "document.body.classList.toggle('chat-app', im);") {
		t.Error("app.js does not give the body the class chat-app on the Telegram-style page")
	}
	still := mediaBlock(t, css, "(prefers-reduced-motion: reduce)")
	if !regexp.MustCompile(`\.chat-app \*,\s*\.chat-app \*::before,\s*\.chat-app \*::after\s*\{[^}]*animation:\s*none !important;`).MatchString(still) {
		t.Error("style.css: reduced motion does not stop the profile card's animation")
	}
	if !regexp.MustCompile(`\.chat-app \.tg-profile-backdrop,\s*\.chat-app \.tg-profile\s*\{[^}]*animation:\s*none;`).MatchString(still) {
		t.Error("style.css: reduced motion does not name the profile card")
	}
}
