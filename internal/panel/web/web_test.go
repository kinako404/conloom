package web

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// assets returns the embedded files with one of exts, keyed by path.
func assets(t *testing.T, exts ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, ext := range exts {
			if path.Ext(p) == ext {
				b, err := fs.ReadFile(FS, p)
				if err != nil {
					return err
				}
				out[p] = string(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAssets_PlannedFilesPresent(t *testing.T) {
	for _, name := range []string{
		"index.html", "style.css", "app.js",
		"lib/api.js", "lib/router.js", "lib/dom.js", "lib/chart.js", "lib/format.js", "lib/sections.js",
		"views/login.js", "views/overview.js", "views/usage.js", "views/conversations.js",
		"views/conversation.js", "views/chat.js", "views/chats.js", "views/chatclassic.js", "views/chatsclassic.js", "views/avatar.js", "views/autonomy.js", "views/tasks.js", "views/audit.js", "views/config.js",
		"views/personaform.js", "views/templates.js",
	} {
		if _, err := fs.Stat(FS, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAssets_IndexPresentNoInlineScript(t *testing.T) {
	b, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`).FindAllStringSubmatch(html, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html loads no script")
	}
	if n := strings.Count(strings.ToLower(html), "<script"); n != len(scripts) {
		t.Fatalf("index.html has %d <script tags but %d complete elements", n, len(scripts))
	}
	src := regexp.MustCompile(`(?i)\ssrc\s*=\s*"[^"]+"`)
	for _, s := range scripts {
		if !src.MatchString(s[1]) {
			t.Errorf("script without src: %q", s[0])
		}
		if strings.TrimSpace(s[2]) != "" {
			t.Errorf("inline script code: %q", s[0])
		}
	}
	for _, bad := range []struct{ name, re string }{
		{"style attribute", `(?i)\sstyle\s*=`},
		{"event handler attribute", `(?i)\son[a-z]+\s*=`},
		{"style element", `(?i)<style\b`},
	} {
		if m := regexp.MustCompile(bad.re).FindString(html); m != "" {
			t.Errorf("index.html: %s %q", bad.name, m)
		}
	}
}

func TestAssets_NoExternalOrUnsafeAPIs(t *testing.T) {
	files := assets(t, ".js", ".html", ".css")
	if len(files) < 3 {
		t.Fatalf("only %d assets embedded", len(files))
	}
	forbidden := []string{
		"http://", "https://",
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
		"eval(", "new Function",
		// The CSP allows images from 'self' and data: only; a local preview
		// is a data: URL from FileReader.
		"blob:", "createObjectURL",
	}
	// The CSP allows no inline styles: no style attribute through h() or
	// setAttribute.
	inlineStyle := regexp.MustCompile(`\bstyle\s*:\s*['"` + "`" + `]|setAttribute\(\s*['"]style['"]`)
	for name, text := range files {
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Errorf("%s contains %q", name, f)
			}
		}
		if path.Ext(name) == ".js" {
			if m := inlineStyle.FindString(text); m != "" {
				t.Errorf("%s sets an inline style: %q", name, m)
			}
		}
	}
}

// QQ's in-app browser on Android (X5, reported at Chrome 89) still opens panel
// links, so the scripts use no API newer than that.
func TestAssets_NoAPIsNewerThanChrome89(t *testing.T) {
	newer := []string{
		"Object.hasOwn(",       // Chrome 93
		"structuredClone(",     // Chrome 98
		".at(",                 // Chrome 92
		"crypto.randomUUID(",   // Chrome 92
		".findLast(",           // Chrome 97
		".findLastIndex(",      // Chrome 97
		"AbortSignal.timeout(", // Chrome 103
		".toSorted(",           // Chrome 110
		".toReversed(",         // Chrome 110
		".toSpliced(",          // Chrome 110
		"AbortSignal.any(",     // Chrome 116
		"Object.groupBy(",      // Chrome 117
		"Map.groupBy(",         // Chrome 117
	}
	for name, text := range assets(t, ".js") {
		for _, api := range newer {
			if strings.Contains(text, api) {
				t.Errorf("%s uses %s", name, strings.TrimSuffix(api, "("))
			}
		}
	}
}

var importRe = regexp.MustCompile(`(?s)\b(?:import|export)\s+(?:[^'";]*?\s+from\s+)?['"]([^'"]+)['"]|\bimport\(\s*['"]([^'"]+)['"]\s*\)`)

func TestAssets_ImportsResolve(t *testing.T) {
	files := assets(t, ".js")
	imports := 0
	for name, text := range files {
		for _, m := range importRe.FindAllStringSubmatch(text, -1) {
			spec := m[1] + m[2]
			imports++
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				t.Errorf("%s imports %q, want a relative path", name, spec)
				continue
			}
			target := path.Join(path.Dir(name), spec)
			if _, err := fs.Stat(FS, target); err != nil {
				t.Errorf("%s imports %q: %s is not embedded", name, spec, target)
			}
		}
	}
	if imports == 0 {
		t.Fatal("no imports found")
	}
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?i)\s(?:src|href)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		if strings.HasPrefix(m[1], "data:") || strings.HasPrefix(m[1], "#") {
			continue
		}
		if _, err := fs.Stat(FS, path.Clean(m[1])); err != nil {
			t.Errorf("index.html references %q, which is not embedded", m[1])
		}
	}
}

// Phone keyboards capitalize and correct a plain text field and may add a
// space after a suggested word; the login name field turns that off and the
// name is sent trimmed.
func TestAssets_LoginNameNotAutocorrected(t *testing.T) {
	js := assets(t, ".js")["views/login.js"]
	m := regexp.MustCompile(`const name = h\('input', \{([^}]*)\}\);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/login.js has no name input")
	}
	for _, attr := range []string{`autocapitalize: 'none'`, `autocorrect: 'off'`, `spellcheck: 'false'`} {
		if !strings.Contains(m[1], attr) {
			t.Errorf("name input lacks %s: {%s}", attr, m[1])
		}
	}
	if !regexp.MustCompile(`\blogin\(name\.value\.trim\(\),`).MatchString(js) {
		t.Error("views/login.js does not send the name trimmed")
	}
}

// mediaBlock returns the rules inside css's @media block for query.
func mediaBlock(t *testing.T, css, query string) string {
	t.Helper()
	head := "@media " + query + " {"
	start := strings.Index(css, head)
	if start < 0 {
		t.Fatalf("style.css has no %q block", head)
	}
	depth := 0
	for i := start + len(head) - 1; i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[start+len(head) : i]
			}
		}
	}
	t.Fatalf("style.css: %q block is not closed", head)
	return ""
}

// On a narrow screen the chat page shows one column, like Telegram on a
// phone: the folders, in a row, and the list without a chat, the chat alone
// with one, which has a way back; nothing points to a list on the left.
func TestAssets_ChatPageOneColumnOnNarrowScreens(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chats.js"]
	m := regexp.MustCompile(`const NARROW = '([^']+)';`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chats.js has no NARROW media query")
	}
	block := mediaBlock(t, files["style.css"], m[1])
	for _, c := range []struct{ name, re string }{
		{"one column", `\.chat-app \.tg\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\);`},
		{"folders in a row", `\.chat-app \.tg-folders\s*\{[^}]*flex-direction:\s*row;`},
		{"no chat pane without a chat", `\.chat-app \.tg:not\(\.open\) \.tg-pane\s*\{\s*display:\s*none;`},
		{"only the chat with one", `\.chat-app \.tg\.open \.tg-rail,\s*\.chat-app \.tg\.open \.tg-list\s*\{\s*display:\s*none;`},
	} {
		if !regexp.MustCompile(c.re).MatchString(block) {
			t.Errorf("style.css @media %s lacks %s (%s)", m[1], c.name, c.re)
		}
	}
	if !strings.Contains(js, "layout.classList.toggle('open', !!key);") {
		t.Error("views/chats.js does not mark the layout open with a chat")
	}
	if !regexp.MustCompile(`onBack[^\n]*go\(withQuery\('#/im', \{ bot: folder \}\)\)`).MatchString(js) {
		t.Error("views/chats.js has no onBack to the list that keeps ?bot=")
	}
	if strings.Contains(js, "从左边") {
		t.Error("views/chats.js points to the list on the left, which is not beside the chat on a narrow screen")
	}
}

// The chat page looks like Telegram Desktop: it fills the window without the
// top bar, has a folder rail, a chat list with unread counts the browser
// keeps and a ✎ button, and an empty pane with a pill; a bot's avatar is its
// persona image cropped round, or its first letter.
func TestChatsView_TelegramShell(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chats.js"]
	for _, want := range []string{
		"'选择一个对话开始聊天'", "'还没有聊天，点右下角的 ✎ 开始'", "const SEEN_KEY = 'conloom.chat.seen';",
		"'你：'", "'99+'", "title: '新建聊天'", "placeholder: '搜索'", "const REFRESH_MS = 5000;",
		"SECTIONS.map(", "'新建 Bot'", "currentUser()", "'退出'",
		// A light poll never drops a full reload, bots changed elsewhere
		// show, and the bot forms open with the bot read afresh.
		"if (fullPending) return;", "if (fullPending || n !== fullLoads) return;", "if (n === fullLoads) showChats(chats);",
		"const BOTS_EVERY = 6;", "await load(signal, !fetched || ++ticks % BOTS_EVERY === 0);",
		"if (n === botReads) {", "const fresh = (all.bots || []).find((b) => b.id === bot.id);",
		"configForm.open(fresh,", "botForm.open(fresh,", "if (fresh) deleteForm.open(fresh);",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	avatar, ok := files["views/avatar.js"]
	if !ok {
		t.Fatal("views/avatar.js is not embedded")
	}
	for _, want := range []string{"api/persona-images/", "?size=reply&v=", "export function avatarEl(bot, size)", "[...name][0]"} {
		if !strings.Contains(avatar, want) {
			t.Errorf("views/avatar.js lacks %q", want)
		}
	}
	css := files["style.css"]
	if !regexp.MustCompile(`\.chat-app \.avatar\s*\{[^}]*object-fit:\s*cover;`).MatchString(css) {
		t.Error("style.css: .chat-app .avatar does not set object-fit: cover")
	}
	for i := 0; i < 8; i++ {
		if !strings.Contains(css, ".chat-app .avatar-c"+strconv.Itoa(i)+" ") {
			t.Errorf("style.css has no avatar color %d", i)
		}
	}
	app := files["app.js"]
	for _, want := range []string{
		"const im = section === 'im';", "document.body.classList.toggle('chat-app', im);",
		"topbar.hidden = section === 'login' || im;", "import { SECTIONS } from './lib/sections.js';",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	// The top bar and the ≡ menu list the same sections, from lib/sections.js,
	// in the panel's order; neither has its own copy.
	m := regexp.MustCompile(`(?s)export const SECTIONS = \[(.*?)\];`).FindStringSubmatch(files["lib/sections.js"])
	if m == nil {
		t.Fatal("lib/sections.js has no SECTIONS")
	}
	var sections []string
	for _, s := range regexp.MustCompile(`\['([^']*)', '([^']*)'\]`).FindAllStringSubmatch(m[1], -1) {
		sections = append(sections, s[1]+"="+s[2])
	}
	if want := []string{"=概览", "usage=用量", "chat=聊天", "conversations=会话", "templates=人设模板", "tasks=任务", "audit=审计", "config=配置"}; !slices.Equal(sections, want) {
		t.Errorf("SECTIONS = %q, want %q", sections, want)
	}
	if !strings.Contains(js, "import { SECTIONS } from '../lib/sections.js';") {
		t.Error("views/chats.js does not take its menu links from lib/sections.js")
	}
	for name, text := range map[string]string{"app.js": app, "views/chats.js": js} {
		if strings.Contains(text, "['usage', '用量']") {
			t.Errorf("%s has its own copy of the sections", name)
		}
		if strings.Contains(text, "app.js'") && name != "app.js" {
			t.Errorf("%s imports app.js", name)
		}
	}
	// Every chat page rule is scoped to .chat-app, so other pages keep their
	// look.
	for _, m := range cssRule.FindAllStringSubmatch(regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, ""), -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if regexp.MustCompile(`\.(tg|avatar)\b|\.tg-`).MatchString(sel) && !strings.HasPrefix(sel, ".chat-app ") && !strings.HasPrefix(sel, "body.chat-app ") {
				t.Errorf("style.css: %q is not scoped to .chat-app", sel)
			}
		}
	}
}

// In Safari the Enter that commits an IME composition comes after
// compositionend, with keyCode 229, and must not send. On a touch screen
// Return types a newline and only 发送 sends.
func TestAssets_ChatEnterSendsOnlyWithKeyboard(t *testing.T) {
	js := assets(t, ".js")["views/chat.js"]
	for _, c := range []struct{ name, re string }{
		{"a coarse-pointer media query", `const TOUCH = '\(pointer: coarse\)';`},
		{"a touch check", `const touch = matchMedia\(TOUCH\)\.matches;`},
		{"Enter-to-send bound only without touch", `if \(!touch\) \{\s*input\.addEventListener\('keydown',`},
		{"the IME keyCode check", `e\.key === 'Enter' && !e\.shiftKey && !e\.isComposing && e\.keyCode !== 229\b`},
	} {
		if !regexp.MustCompile(c.re).MatchString(js) {
			t.Errorf("views/chat.js lacks %s (%s)", c.name, c.re)
		}
	}
	m := regexp.MustCompile(`placeholder: touch \? '([^']*)' : '([^']*)'`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chat.js has no placeholder for touch screens")
	}
	if strings.Contains(m[1], "回车") {
		t.Errorf("touch placeholder %q mentions the Enter key", m[1])
	}
}

// The chat pane looks like Telegram: a top bar with ← back on a narrow
// screen, the bot, 正在输入… and a ⋮ menu holding 会话详情, the 建议回复
// switch and 删除这个聊天; bubbles left and right with the time inside, a
// tail on the last of a group and no 面板 tag; date pills between days; and a
// growing box with a round ➤ button. The pane's own top bar replaces the one
// views/chats.js had.
func TestChatPane_TelegramStyle(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chat.js"]
	for _, want := range []string{
		"'正在输入…'", "'会话详情'", "'建议回复：开'", "'建议回复：关'", "'删除这个聊天'", "'➤'", "title: '发送'",
		"onBack", "onDelete", "avatarEl(bot, 40)", "import { avatarEl } from './avatar.js';",
		"m.channel && m.channel !== 'panel'", "`${d.getMonth() + 1}月${d.getDate()}日`", "`${d.getFullYear()}年${",
		"const MAX_LINES = 6;", "send.disabled = true;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	for _, gone := range []string{"'面板'", "type: 'checkbox'", "'对方正在输入…'"} {
		if strings.Contains(js, gone) {
			t.Errorf("views/chat.js still has %q", gone)
		}
	}
	chats := files["views/chats.js"]
	for _, gone := range []string{"chatHead", "tg-head", "tg-back"} {
		if strings.Contains(chats, gone) {
			t.Errorf("views/chats.js still has the interim header (%q)", gone)
		}
	}
	css := files["style.css"]
	for _, gone := range []string{".tg-head", ".tg-back"} {
		if strings.Contains(css, gone) {
			t.Errorf("style.css still has %q", gone)
		}
	}
	for _, c := range []struct{ name, re string }{
		{"a tail on the last bubble of a group", `\.chat-app \.tg-msg\.tail \.tg-bubble::after\s*\{`},
		{"a round send button", `\.chat-app \.tg-send\s*\{[^}]*border-radius:\s*50%;`},
		{"own bubbles on the right", `\.chat-app \.tg-msg\.out\s*\{[^}]*align-self:\s*flex-end;`},
		{"full-width suggestion buttons", `\.chat-app \.tg-keys button\s*\{[^}]*width:\s*100%;`},
		{"no ← on a wide screen", `\.chat-app \.tg-bar-back\s*\{[^}]*display:\s*none;`},
	} {
		if !regexp.MustCompile(c.re).MatchString(css) {
			t.Errorf("style.css lacks %s (%s)", c.name, c.re)
		}
	}
	m := regexp.MustCompile(`const NARROW = '([^']+)';`).FindStringSubmatch(chats)
	if m == nil {
		t.Fatal("views/chats.js has no NARROW media query")
	}
	if !regexp.MustCompile(`\.chat-app \.tg-bar-back\s*\{[^}]*display:\s*inline-flex;`).MatchString(mediaBlock(t, css, m[1])) {
		t.Errorf("style.css @media %s does not show ← back to the list", m[1])
	}
}

var cssRule = regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)

// iOS Safari zooms in on a focused field whose text is under 16px and does not
// zoom back out, so on a narrow screen every rule that sizes the text of a
// form control is overridden to 16px.
func TestAssets_FormControlsAre16pxOnNarrowScreens(t *testing.T) {
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(assets(t, ".css")["style.css"], "")
	block := mediaBlock(t, css, "(max-width: 699px)")
	big := map[string]bool{}
	for _, m := range cssRule.FindAllStringSubmatch(block, -1) {
		if regexp.MustCompile(`font-size:\s*16px`).MatchString(m[2]) {
			for _, sel := range strings.Split(m[1], ",") {
				big[strings.TrimSpace(sel)] = true
			}
		}
	}
	control := regexp.MustCompile(`(?:^|[\s>+~])(?:input|select|textarea)(?:$|[.\[:])[^\s>+~]*$`)
	sized := regexp.MustCompile(`(?:^|[;\s])font(?:-size)?\s*:`)
	checked := 0
	for _, m := range cssRule.FindAllStringSubmatch(strings.Replace(css, block, "", 1), -1) {
		if !sized.MatchString(m[2]) {
			continue
		}
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if !control.MatchString(sel) {
				continue
			}
			checked++
			if !big[sel] {
				t.Errorf("style.css: %q sets a font size but is not 16px on a narrow screen", sel)
			}
		}
	}
	if checked == 0 {
		t.Fatal("style.css: found no rule sizing the text of a form control")
	}
}

// Every dialog is also a card, and .card sets margin-bottom, which overrides
// the margin: auto that centers a modal dialog; dialog.dialog puts it back.
func TestAssets_DialogsCentered(t *testing.T) {
	files := assets(t, ".js", ".css")
	dialogs := 0
	for name, text := range files {
		for _, m := range regexp.MustCompile(`h\('dialog', \{ class: '([^']*)'`).FindAllStringSubmatch(text, -1) {
			dialogs++
			if classes := strings.Fields(m[1]); !slices.Contains(classes, "dialog") {
				t.Errorf("%s: dialog with classes %q lacks the dialog class", name, m[1])
			}
		}
	}
	if dialogs == 0 {
		t.Fatal("no dialogs found")
	}
	centered := false
	for _, m := range cssRule.FindAllStringSubmatch(files["style.css"], -1) {
		if strings.TrimSpace(m[1]) == "dialog.dialog" && regexp.MustCompile(`(?:^|[;\s])margin:\s*auto;`).MatchString(m[2]) {
			centered = true
		}
	}
	if !centered {
		t.Error("style.css: dialog.dialog does not set margin: auto")
	}
}

func TestAssets_InitialAffinityMatchesMemory(t *testing.T) {
	js := assets(t, ".js")["views/conversation.js"]
	m := regexp.MustCompile(`const INITIAL_AFFINITY = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/conversation.js has no INITIAL_AFFINITY")
	}
	if want := strconv.Itoa(memory.InitialAffinity); m[1] != want {
		t.Errorf("INITIAL_AFFINITY = %s, want memory.InitialAffinity %s", m[1], want)
	}
}

func TestAssets_MaxBarsMatchesPersona(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	m := regexp.MustCompile(`const MAX_BARS = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/personaform.js has no MAX_BARS")
	}
	if want := strconv.Itoa(conloomapi.MaxPersonaBars); m[1] != want {
		t.Errorf("MAX_BARS = %s, want conloomapi.MaxPersonaBars %s", m[1], want)
	}
}

func TestAssets_GenderOptionsMatchPersona(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	m := regexp.MustCompile(`const GENDER = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/personaform.js has no GENDER")
	}
	var got []string
	for _, k := range regexp.MustCompile(`'?(\w*)'?: '`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, k[1])
	}
	if want := []string{"", conloomapi.GenderFemale, conloomapi.GenderMale}; !slices.Equal(got, want) {
		t.Errorf("GENDER keys = %q, want %q", got, want)
	}
	if !strings.Contains(js, "persona.gender = gender.value") {
		t.Error("views/personaform.js does not save the gender select")
	}
}

// listItems returns the quoted items of personaform.js's array constant name.
func listItems(t *testing.T, js, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`const ` + name + ` = \[(.*?)\];`).FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("views/personaform.js has no %s", name)
	}
	var items []string
	for _, v := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		items = append(items, v[1])
	}
	return items
}

// The persona form edits the intimacy side: three texts counted together, in
// characters after trimming, against conloomapi.MaxIntimacyRunes, and a level
// per affinity tier from the lowest, the default levels when the persona has
// none. A minor persona gets the section disabled, a persona that is not
// adult a note; saving refuses what the server would.
// TestPersonaForm_IntimacyBounds checks the bounds.
func TestPersonaForm_IntimacySection(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	m := regexp.MustCompile(`const INTIMACY_LEVELS = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/personaform.js has no INTIMACY_LEVELS")
	}
	var keys, labels []string
	for _, k := range regexp.MustCompile(`(\w+): '([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		keys, labels = append(keys, k[1]), append(labels, k[2])
	}
	if want := []string{conloomapi.IntimacyNone, conloomapi.IntimacyLight, conloomapi.IntimacyHalf, conloomapi.IntimacyMajor}; !slices.Equal(keys, want) {
		t.Errorf("INTIMACY_LEVELS keys = %q, want %q", keys, want)
	}
	if want := []string{"不混", "少量", "一半", "为主"}; !slices.Equal(labels, want) {
		t.Errorf("INTIMACY_LEVELS labels = %q, want %q", labels, want)
	}
	if got, want := listItems(t, js, "DEFAULT_INTIMACY_LEVELS"), conloomapi.DefaultIntimacyLevels(); !slices.Equal(got, want) {
		t.Errorf("DEFAULT_INTIMACY_LEVELS = %q, want conloomapi.DefaultIntimacyLevels %q", got, want)
	}
	if got, want := listItems(t, js, "INTIMACY_TIERS"), memory.TierNames(); !slices.Equal(got, want) {
		t.Errorf("INTIMACY_TIERS = %q, want the affinity tiers %q", got, want)
	}
	limit := regexp.MustCompile(`const MAX_INTIMACY = (\d+);`).FindStringSubmatch(js)
	if limit == nil {
		t.Fatal("views/personaform.js has no MAX_INTIMACY")
	}
	if want := strconv.Itoa(conloomapi.MaxIntimacyRunes); limit[1] != want {
		t.Errorf("MAX_INTIMACY = %s, want conloomapi.MaxIntimacyRunes %s", limit[1], want)
	}

	shown := strings.ReplaceAll(js, "${MAX_INTIMACY}", limit[1])
	for _, want := range []string{
		"h('h3', null, '亲密面')", "field('语气', tone)", "field('心理', mind)", "field('表现', behavior)",
		"`合计 ${n} / 800 字`", "[...t.value.trim()].length",
		"'只在成年人设的私聊里生效；群聊和保护模式下不使用。改模板不影响已有会话，要到会话的人设页套用模板。'",
		"'未成年人设不能设置亲密面'", "'当前不是成年人设，亲密面不会生效'",
		"'未成年人设不能设置亲密面，请先清空'", "`亲密面合计最多 800 字`", "'亲密面的档位要从低到高不递减'",
		"section.disabled = minor.checked;", "minorNote.hidden = !minor.checked;",
		"notAdultNote.hidden = minor.checked || adult.checked;",
		"'清空亲密面'", "for (const t of texts) t.value = '';",
		"minor.addEventListener('change', sync);", "adult.addEventListener('change', sync);",
		"const intimacy = intimacyEditor(base, { minor, adult });", "h('h3', null, '亲密面'),\n  intimacy.el,",
		"const side = intimacy.read();\n    if (side.error) return { error: side.error };",
		"if (side.value) persona.intimacy = side.value;\n    else delete persona.intimacy;",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("views/personaform.js lacks %q", want)
		}
	}
}

// Each affinity tier of the intimacy side has an optional bound, a full-width
// row named after the tier under the level selects, saved as intimacy.bounds,
// whose placeholder and title are the tier's built-in bound from GET
// affinity-bounds, read once when the form opens. Line breaks
// a bound brings are removed, not refused; saving refuses a bound over
// conloomapi.MaxIntimacyBoundRunes after trimming. The three texts and the
// bounds all blank save no intimacy side, and blank bounds no bounds; bounds
// alone keep it. A minor persona's bounds count as text: the clear button
// shows for them and empties them, and saving refuses them.
func TestPersonaForm_IntimacyBounds(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	limit := regexp.MustCompile(`const MAX_INTIMACY_BOUND = (\d+);`).FindStringSubmatch(js)
	if limit == nil {
		t.Fatal("views/personaform.js has no MAX_INTIMACY_BOUND")
	}
	if want := strconv.Itoa(conloomapi.MaxIntimacyBoundRunes); limit[1] != want {
		t.Errorf("MAX_INTIMACY_BOUND = %s, want conloomapi.MaxIntimacyBoundRunes %s", limit[1], want)
	}
	if n := strings.Count(js, "'affinity-bounds'"); n != 1 {
		t.Errorf("views/personaform.js names 'affinity-bounds' %d times, want one call", n)
	}

	shown := strings.ReplaceAll(js, "${MAX_INTIMACY_BOUND}", limit[1])
	for _, want := range []string{
		"get('affinity-bounds')", "b.placeholder = text;\n    b.title = text;",
		"showDefault(b, defaults[i] || '')", "showDefault(b, '空着就用内置边界')",
		"const savedBounds = doc.bounds || [];", "h('input', { type: 'text', value: savedBounds[i] || '' })",
		"h('div', { class: 'form-grid' }, INTIMACY_TIERS.map((tier, i) => field(tier, levels[i]))),\n" +
			"    INTIMACY_TIERS.map((tier, i) => field(`${tier} · 边界（可选）`, bounds[i])));",
		"'边界替换这一档内置的分寸说明，空着就用内置的；它不能越过内容规则。'",
		".value.replace(/[\\r\\n]/g, '')", "[...v.trim()].length > MAX_INTIMACY_BOUND", "`每档边界最多 150 字`",
		"clearBar.hidden = !minor.checked || (total() === 0 && !hasBound());",
		"for (const b of bounds) b.value = '';", "for (const b of bounds) b.addEventListener('input', sync);",
		"if (n === 0 && !hasBound()) return { value: undefined };\n    if (minor.checked) return { error: '未成年人设不能设置亲密面，请先清空' };",
		"if (hasBound()) value.bounds = written;",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("views/personaform.js lacks %q", want)
		}
	}
}

// A config bot's form offers exactly the private ranges the core knows, sends
// every setting PUT bots/{id} requires of a config bot, one platform ID a
// line, and only shows the identity, which is changed in the config file.
func TestAssets_ConfigBotForm(t *testing.T) {
	js := assets(t, ".js")["views/chats.js"]
	m := regexp.MustCompile(`(?s)const PRIVATE_CHAT = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chats.js has no PRIVATE_CHAT")
	}
	var got []string
	for _, k := range regexp.MustCompile(`(?m)^\s*(\w+):`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, k[1])
	}
	if want := config.PrivateChatModes(); !slices.Equal(got, want) {
		t.Errorf("PRIVATE_CHAT keys = %v, want config.PrivateChatModes %v", got, want)
	}
	for _, want := range []string{
		"name: name.input.value.trim(),\n          admins: ids(admins)", "private_chat: privateChat.value",
		"private_allowlist: ids(allowlist)", "groups: ids(groups)", "private_template: privateTemplate.value",
		"group_template: groupTemplate.value", "skills: checkedSkills(boxes)",
		".value.split('\\n').map((s) => s.trim()).filter(Boolean)", "(list || []).join('\\n')",
		"['ID', bot.id], ['平台', bot.platform], ['平台自身 ID', bot.platform_self_id]", "在配置文件里修改'",
		"skills: res.config_skills || []", "bot.source === PANEL ? deleteButton(bot) : null",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
}

func TestAssets_EventKindsMatchStore(t *testing.T) {
	js := assets(t, ".js")["views/autonomy.js"]
	m := regexp.MustCompile(`(?s)const EVENT_KIND = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/autonomy.js has no EVENT_KIND")
	}
	var got []string
	for _, k := range regexp.MustCompile(`(?m)^\s*(\w+):`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, k[1])
	}
	want := []string{store.BuffKindAdjust, store.BuffKindBarAdd, store.BuffKindBarRemove, store.BuffKindEffectAdd, store.BuffKindEffectRemove}
	if !slices.Equal(got, want) {
		t.Errorf("EVENT_KIND keys = %v, want the store's kinds %v", got, want)
	}
}

func TestAssets_AppearanceLimitsMatchServer(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	for _, c := range []struct {
		name string
		want int
	}{
		{"MAX_APPEARANCE", conloomapi.MaxAppearanceRunes},
		{"MAX_IMAGE_MIB", imaging.MaxBytes >> 20},
	} {
		m := regexp.MustCompile(`const ` + c.name + ` = (\d+);`).FindStringSubmatch(js)
		if m == nil {
			t.Errorf("views/personaform.js has no %s", c.name)
			continue
		}
		if want := strconv.Itoa(c.want); m[1] != want {
			t.Errorf("%s = %s, want %s", c.name, m[1], want)
		}
	}
}

// The chat offers reply suggestions behind a 建议回复 switch the browser keeps,
// on unless turned off; it asks for those after the newest message, drops an
// answer that is no longer wanted, also once the user answered that message,
// sends a suggestion when clicked, keeps one that failed to send in the box,
// and drops the suggestions when the user types or a message comes.
func TestAssets_ChatSuggestions(t *testing.T) {
	js := assets(t, ".js")["views/chat.js"]
	for _, want := range []string{
		"const SUGGEST_KEY = 'conloom.chat.suggestions';", "localStorage.getItem(SUGGEST_KEY) !== SUGGEST_OFF",
		"localStorage.setItem(SUGGEST_KEY, SUGGEST_OFF)", "let suggestOn = suggestionsOn();", "saveSuggestionsOn(suggestOn);",
		"suggestOn ? '建议回复：开' : '建议回复：关'", "suggestOn && !gone",
		"post(convPath(key, 'suggestions'), { after_seq: seq })", "last.seq === seq", "!botTyping", "last.from_bot",
		"seq !== sentAfter", "if (last !== null) sentAfter = last.seq;",
		"if (!items.length || !wanted(seq)) return;", "onclick: () => submit(text)",
		"if (!input.value.trim()) input.value = text;",
		"input.addEventListener('input', clearSuggestions)", "if (!added.length) clearSuggestions();",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
}

// Every purpose a model call is recorded with has a label on the usage page.
func TestAssets_PurposeLabelsMatchStore(t *testing.T) {
	js := assets(t, ".js")["lib/format.js"]
	m := regexp.MustCompile(`(?s)export const PURPOSE = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("lib/format.js has no PURPOSE")
	}
	var got []string
	for _, k := range regexp.MustCompile(`(?m)^\s*(\w+):`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, k[1])
	}
	want := []string{store.PurposeChat, store.PurposeMemory, store.PurposeEmbedIndex, store.PurposeEmbedQuery,
		store.PurposeVision, store.PurposeSuggest}
	if !slices.Equal(got, want) {
		t.Errorf("PURPOSE keys = %v, want the store's purposes %v", got, want)
	}
	if !strings.Contains(m[1], "suggest: '建议回复'") {
		t.Errorf("PURPOSE does not label suggest 建议回复: %s", m[1])
	}
}

// A message stored without a reply says why under the chat box, by the
// suppressed_reason of the 202 to POST messages.
func TestAssets_ChatShowsSuppressedReason(t *testing.T) {
	js := assets(t, ".js")["views/chat.js"]
	if !strings.Contains(js, "res.suppressed_reason") {
		t.Error("views/chat.js does not read suppressed_reason from the POST messages response")
	}
	m := regexp.MustCompile(`(?s)const SUPPRESSED = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chat.js has no SUPPRESSED")
	}
	for _, reason := range []string{conloomapi.SuppressedPaused, conloomapi.SuppressedRateLimited, conloomapi.SuppressedQueueFull} {
		if !regexp.MustCompile(`(?m)^\s*` + reason + `:`).MatchString(m[1]) {
			t.Errorf("SUPPRESSED has no note for %s", reason)
		}
	}
}

// jsBlock returns the text of js from head to the brace that closes the first
// block after it.
func jsBlock(t *testing.T, js, head string) string {
	t.Helper()
	start := strings.Index(js, head)
	if start < 0 {
		t.Fatalf("no %q", head)
	}
	depth := 0
	for i := start; i < len(js); i++ {
		switch js[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return js[start : i+1]
			}
		}
	}
	t.Fatalf("%q is not closed", head)
	return ""
}

// The chat open counts as seen as messages arrive in it, the user's own and
// the bot's replies, not only when the list is read again: the pane hands
// the page every message it shows, and the page counts those the list read
// last does not have yet.
func TestChatsView_UnreadFollowsTheOpenChat(t *testing.T) {
	files := assets(t, ".js")
	chat, chats := files["views/chat.js"], files["views/chats.js"]
	for _, want := range []string{
		"export async function chatPane({ key, signal, hint, bot, title, onBack, onDelete, onMessages })",
		"if (onMessages && !signal.aborted) onMessages(added);",
	} {
		if !strings.Contains(chat, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	for _, want := range []string{
		"chatPane({ key, signal, hint: PANEL_HINT, bot, title, onBack, onDelete, onMessages })",
		"if (!signal.aborted) noteArrived(key, messages);",
		"(arrived[c.key] || []).filter((m) => !c.last || m.seq > c.last.seq)",
		"seen = noteSeen(allChats(), current);",
	} {
		if !strings.Contains(chats, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	// noteSeen forgets the chats it is not given: it never runs on the empty
	// list of a page that has not read it yet.
	if !strings.Contains(chats, "if (key && fetched) seen = noteSeen(allChats(), key);") {
		t.Error("views/chats.js notes seen on opening a chat before the list is read")
	}
}

// The chat page shows its rail, ≡ menu, list and pane before reading the
// list, so a failed first read leaves the navigation there; the error is
// shown in the list and the poll keeps trying, at once the first time.
func TestChatsView_ShellBeforeTheList(t *testing.T) {
	js := assets(t, ".js")["views/chats.js"]
	view := jsBlock(t, js, "export function chatsView(ctx) {")
	for _, want := range []string{"ctx.show(page.layout);", "page.select(ctx.params.key || '', ctx.signal);", "page.watch(ctx.signal);"} {
		if !strings.Contains(view, want) {
			t.Errorf("chatsView lacks %q", want)
		}
	}
	if strings.Contains(view, "await") || strings.Contains(view, "load(") {
		t.Errorf("chatsView waits for the list before showing the page:\n%s", view)
	}
	watch := jsBlock(t, js, "function watch(signal) {")
	for _, want := range []string{
		"timer = setTimeout(tick, fetched ? REFRESH_MS : 0);",
		"if (!isAbort(err) && !signal.aborted) mount(pollMsg, errorBox(err));",
		"if (fetched && pending === signal) select(current, signal);",
	} {
		if !strings.Contains(watch, want) {
			t.Errorf("watch lacks %q", want)
		}
	}
}

// Reading the list again renders it only when what it shows changed, so the
// focus, a click under way and the avatars stay; and the list is not read
// while the page is hidden, but at once when it shows again.
func TestChatsView_ListPollQuiet(t *testing.T) {
	js := assets(t, ".js")["views/chats.js"]
	for _, want := range []string{
		"const listState = () => JSON.stringify([groups, seen, bots, shownFolder(), current, new Date().toDateString()]);",
		"rendered = listState();", "if (listState() !== rendered) renderLists();",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	load := jsBlock(t, js, "async function load(signal, withBots = true) {")
	if strings.Contains(load, "renderLists()") || strings.Count(load, "renderChanged();") != 2 {
		t.Errorf("load does not render only on change:\n%s", load)
	}
	watch := jsBlock(t, js, "function watch(signal) {")
	for _, want := range []string{
		"if (signal.aborted || running || document.hidden) return;",
		"if (!document.hidden) timer = setTimeout(tick, REFRESH_MS);",
		"document.addEventListener('visibilitychange', onVisibility);",
		"document.removeEventListener('visibilitychange', onVisibility);",
	} {
		if !strings.Contains(watch, want) {
			t.Errorf("watch lacks %q", want)
		}
	}
}

// A bubble names its sender on hover, and bubbles group by sender as well as
// side; the list prefixes 你： only to the panel user's own messages, whose
// sender ID is built as core.PanelActorID builds it. A folder whose bot has
// problems shows a warn dot listing them. Escape anywhere closes the ⋮ menu,
// and a resized window refits the message box. Listeners end with the
// signal by hand: addEventListener's signal option is newer than Chrome 89.
func TestChatPane_SenderWarnAndKeys(t *testing.T) {
	files := assets(t, ".js", ".css")
	chat, chats := files["views/chat.js"], files["views/chats.js"]
	for _, want := range []string{
		"title: m.sender_name ? `${m.sender_name} · ${when}` : when",
		"last.from_bot === m.from_bot && last.sender_id === m.sender_id",
		"listen(document, 'keydown', (e) => {", "listen(window, 'resize', grow, signal);",
		"signal.addEventListener('abort', () => target.removeEventListener(type, fn));",
	} {
		if !strings.Contains(chat, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	for name, text := range map[string]string{"views/chat.js": chat, "views/chats.js": chats} {
		if strings.Contains(text, "{ signal })") {
			t.Errorf("%s passes a signal to addEventListener", name)
		}
	}
	for _, want := range []string{
		"const mine = !c.last.from_bot && c.last.sender_id === panelActorID(currentUser());",
		"return 'panel_' + String(user || '').replace(/:/g, '_');",
		"h('span', { class: 'tg-folder-warn', role: 'img', 'aria-label': '设置有问题', title: problems.join('\\n') })",
	} {
		if !strings.Contains(chats, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	if got := core.PanelActorID("a:b"); got != "panel_a_b" {
		t.Errorf("core.PanelActorID(a:b) = %q, which views/chats.js panelActorID does not build", got)
	}
	if !regexp.MustCompile(`\.chat-app \.tg-folder-warn\s*\{[^}]*background:\s*var\(--warn\);`).MatchString(files["style.css"]) {
		t.Error("style.css has no warn dot for a folder")
	}
}

// classicClasses are the classes only the classic chat page uses.
var classicClasses = []string{
	"chat", "chat-log", "bubble", "chat-typing", "chat-form", "chat-input", "chat-suggestions", "chat-options",
	"chat-page", "chat-side", "chat-groups", "chat-group", "chat-group-head", "chat-items", "chat-title", "chat-time",
	"chat-main", "chat-head", "chat-empty", "chat-classic-title",
}

// classNames returns the classes js gives elements through class: '...'.
func classNames(js string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`class: '([^']*)'`).FindAllStringSubmatch(js, -1) {
		for _, c := range strings.Fields(m[1]) {
			out[c] = true
		}
	}
	for _, m := range regexp.MustCompile(`'([\w-]+) (?:bot|user)'`).FindAllStringSubmatch(js, -1) {
		out[m[1]] = true
	}
	return out
}

// The classic chat page stays at #/chat for debugging, under the top bar: 聊天
// with 新建 Bot and 刷新, the bots with their chats on the left, the chat on the
// right, scrolled into view on a narrow screen. It shares the dialogs of the
// Telegram-style page, which moved to #/im, and links to it beside its
// heading, on the chat open.
func TestChatsClassic_Page(t *testing.T) {
	files := assets(t, ".js", ".css")
	js, ok := files["views/chatsclassic.js"]
	if !ok {
		t.Fatal("views/chatsclassic.js is not embedded")
	}
	for _, want := range []string{
		"export async function chatsClassicView(ctx) {", "h('div', { class: 'chat-classic' },", "h('h1', null, '聊天')",
		"'新建 Bot'", "'刷新'", "'新建聊天'", "'会话详情'", "'还没有聊天'", "function chatHead(key, title, botName) {",
		"import { PANEL, PANEL_HINT, newChatDialog, botDialog, configBotDialog, deleteBotDialog } from './chats.js';",
		"import { chatPane } from './chatclassic.js';", "chatPane({ key, signal, hint: PANEL_HINT })",
		"h('a', { class: 'button small', href: '#/im',", "'TG 风格界面 ↗'", "tgLink.href = key ? href('im', key) : '#/im';",
		"h('h1', null, '聊天'),\n      tgLink),", "go(href('chat', key))", "go('#/chat')",
		"if (matchMedia(NARROW).matches) pane.scrollIntoView();",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chatsclassic.js lacks %q", want)
		}
	}
	for _, own := range []string{"function newChatDialog(", "function botDialog(", "function configBotDialog(", "function deleteBotDialog(", "const PRIVATE_CHAT", "const PANEL_HINT"} {
		if strings.Contains(js, own) {
			t.Errorf("views/chatsclassic.js has its own %q", own)
		}
	}
	chats := files["views/chats.js"]
	for _, want := range []string{
		"export const PANEL = 'panel';", "export const PANEL_HINT = ", "export function newChatDialog(created) {",
		"export function botDialog(saved) {", "export function configBotDialog(saved) {", "export function deleteBotDialog(done) {",
	} {
		if !strings.Contains(chats, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	m := regexp.MustCompile(`const NARROW = '([^']+)';`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chatsclassic.js has no NARROW media query")
	}
	block := mediaBlock(t, files["style.css"], m[1])
	if !regexp.MustCompile(`\.chat-classic \.chat-page\s*\{\s*grid-template-columns:\s*1fr;`).MatchString(block) {
		t.Errorf("style.css @media %s does not stack the classic chat page", m[1])
	}
}

// The classic chat pane keeps its bubbles with sender, time and 面板 tag,
// 对方正在输入…, a 发送 button and the 建议回复 checkbox; it shares the
// timing, the switch's storage and the notes with views/chat.js.
func TestChatClassic_Pane(t *testing.T) {
	files := assets(t, ".js")
	js, ok := files["views/chatclassic.js"]
	if !ok {
		t.Fatal("views/chatclassic.js is not embedded")
	}
	for _, want := range []string{
		"export async function chatPane({ key, signal, hint }) {",
		"import { POLL_MS, HISTORY, PAGE, TOUCH, GONE, suggestionsOn, saveSuggestionsOn, suppressedNote } from './chat.js';",
		"m.from_bot ? 'bubble bot' : 'bubble user'", "h('strong', null, m.sender_name || m.sender_id)",
		"m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null", "'对方正在输入…'",
		"h('button', { type: 'submit', class: 'primary' }, '发送')", "h('input', { type: 'checkbox', checked: suggestionsOn() })",
		"toggle, '建议回复'", "saveSuggestionsOn(toggle.checked);", "toggle.checked && !gone",
		"post(convPath(key, 'suggestions'), { after_seq: seq })", "if (!added) clearSuggestions();",
		"if (res && res.suppressed_reason) mount(sendError, suppressedNote(res.suppressed_reason));",
		"const touch = matchMedia(TOUCH).matches;", "e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chatclassic.js lacks %q", want)
		}
	}
	if strings.Contains(js, "SUGGEST_KEY") {
		t.Error("views/chatclassic.js keeps its own 建议回复 switch")
	}
	chat := files["views/chat.js"]
	for _, want := range []string{
		"export const POLL_MS = ", "export const HISTORY = ", "export const PAGE = ", "export const TOUCH = ", "export const GONE = ",
		"export function suggestionsOn() {", "export function saveSuggestionsOn(on) {", "export function suppressedNote(reason) {",
	} {
		if !strings.Contains(chat, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
}

// #/chat is the classic chat page, under the top bar; #/im the Telegram-style
// one, full window. Every link of the Telegram-style page stays on #/im but
// the ≡ menu's 经典聊天页, which opens the classic page on the chat open. The
// conversation page opens a panel chat on the classic page.
func TestChatRoutes_ClassicAndIm(t *testing.T) {
	files := assets(t, ".js")
	app := files["app.js"]
	for _, want := range []string{
		"import { chatsClassicView } from './views/chatsclassic.js';", "import { chatsView } from './views/chats.js';",
		"route('/chat', chatsClassicView);", "route('/chat/:key', chatsClassicView);",
		"route('/im', chatsView);", "route('/im/:key', chatsView);",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(app, "route('/chat', chatsView)") || strings.Contains(app, "section === 'chat'") {
		t.Error("app.js still gives #/chat to the Telegram-style page")
	}
	chats := files["views/chats.js"]
	for _, want := range []string{
		"const classicLink = h('a', { href: '#/chat' }, '经典聊天页');",
		"(section === 'chat' ? classicLink : h('a', { href: '#/' + section }, title))",
		"classicLink.href = key ? href('chat', key) : '#/chat';",
		"withQuery(href('im', key), { bot: folder })", "withQuery(current ? href('im', current) : '#/im', { bot: id })",
		"go(withQuery(href('im', key), { bot: folder && f ? f.group.id : folder }));",
	} {
		if !strings.Contains(chats, want) {
			t.Errorf("views/chats.js lacks %q", want)
		}
	}
	chatRoute := regexp.MustCompile(`'#/chat|href\('chat'`)
	for name, text := range map[string]string{"views/chats.js": chats, "views/chat.js": files["views/chat.js"]} {
		for i, line := range strings.Split(text, "\n") {
			if chatRoute.MatchString(line) && !strings.Contains(line, "classicLink") {
				t.Errorf("%s:%d links to the classic page: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	conv := files["views/conversation.js"]
	if !strings.Contains(conv, "h('a', { class: 'button', href: href('chat', c.key) }, '在聊天页打开')") {
		t.Error("views/conversation.js does not open a panel chat on the classic chat page")
	}
}

// The two chat pages cannot restyle each other: the classic page's rules hang
// on .chat-classic, the Telegram-style page's on .chat-app, no rule mixes
// them, and neither page's markup uses a class only the other's rules style.
func TestChatPages_StylesScoped(t *testing.T) {
	files := assets(t, ".js", ".css")
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(files["style.css"], "")
	names := strings.Join(classicClasses, "|")
	classic := regexp.MustCompile(`\.(?:` + names + `)(?:[^\w-]|$)`)
	scoped := 0
	for _, m := range cssRule.FindAllStringSubmatch(css, -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			isApp := strings.HasPrefix(sel, ".chat-app ") || strings.HasPrefix(sel, "body.chat-app ")
			isClassic := strings.HasPrefix(sel, ".chat-classic ")
			if strings.Contains(sel, "chat-classic") && !isClassic {
				t.Errorf("style.css: %q mentions .chat-classic but is not scoped to it", sel)
			}
			if isClassic {
				scoped++
				if strings.Contains(sel, "chat-app") || strings.Contains(sel, ".tg") {
					t.Errorf("style.css: %q mixes the two chat pages", sel)
				}
			}
			if !isApp && !isClassic && classic.MatchString(sel) {
				t.Errorf("style.css: %q styles the classic chat page but is not scoped to .chat-classic", sel)
			}
		}
	}
	if scoped < len(classicClasses) {
		t.Errorf("style.css has %d rules under .chat-classic, want at least %d", scoped, len(classicClasses))
	}
	for _, c := range classicClasses {
		if c == "chat-groups" {
			continue // the list's box, which has no rule of its own
		}
		if !regexp.MustCompile(`\.chat-classic [^{]*\.` + regexp.QuoteMeta(c) + `(?:[^\w-]|$)`).MatchString(css) {
			t.Errorf("style.css has no .chat-classic rule for .%s", c)
		}
	}
	classicJS := classNames(files["views/chatsclassic.js"] + files["views/chatclassic.js"])
	for _, c := range append(slices.Clone(classicClasses), "chat-classic") {
		if !classicJS[c] {
			t.Errorf("the classic chat page does not use the class %q", c)
		}
	}
	for c := range classicJS {
		if strings.HasPrefix(c, "tg") || c == "avatar" || c == "chat-app" {
			t.Errorf("the classic chat page uses the class %q of the Telegram-style page", c)
		}
	}
	for c := range classNames(files["views/chats.js"] + files["views/chat.js"] + files["views/avatar.js"]) {
		if slices.Contains(classicClasses, c) || c == "chat-classic" {
			t.Errorf("the Telegram-style chat page uses the class %q of the classic page", c)
		}
	}
}
