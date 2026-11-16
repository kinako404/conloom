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
		"lib/api.js", "lib/router.js", "lib/dom.js", "lib/chart.js", "lib/format.js",
		"views/login.js", "views/overview.js", "views/usage.js", "views/conversations.js",
		"views/conversation.js", "views/chat.js", "views/chats.js", "views/autonomy.js", "views/tasks.js", "views/audit.js", "views/config.js",
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

// On a narrow screen style.css puts the chat list above the chat, so opening
// a chat scrolls it into view, and the empty pane does not point left.
func TestAssets_ChatScrollsIntoViewOnNarrowScreens(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chats.js"]
	m := regexp.MustCompile(`const NARROW = '([^']+)';`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/chats.js has no NARROW media query")
	}
	block := mediaBlock(t, files["style.css"], m[1])
	if !regexp.MustCompile(`\.chat-page\s*\{\s*grid-template-columns:\s*1fr;`).MatchString(block) {
		t.Errorf("style.css @media %s does not stack the chat page", m[1])
	}
	if !regexp.MustCompile(`if \(matchMedia\(NARROW\)\.matches\) pane\.scrollIntoView\(\);`).MatchString(js) {
		t.Error("views/chats.js does not scroll the chat into view on a narrow screen")
	}
	if strings.Contains(js, "从左边") {
		t.Error("views/chats.js points to the list on the left, which is above the chat on a narrow screen")
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
		"name: name.value.trim()", "admins: ids(admins)", "private_chat: privateChat.value",
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
// answer that is no longer wanted, sends a suggestion when clicked and drops
// the suggestions when the user types or a message comes.
func TestAssets_ChatSuggestions(t *testing.T) {
	js := assets(t, ".js")["views/chat.js"]
	for _, want := range []string{
		"const SUGGEST_KEY = 'conloom.chat.suggestions';", "localStorage.getItem(SUGGEST_KEY) !== SUGGEST_OFF",
		"localStorage.setItem(SUGGEST_KEY, SUGGEST_OFF)", "checked: suggestionsOn()", "toggle, '建议回复'",
		"post(convPath(key, 'suggestions'), { after_seq: seq })", "last.seq === seq", "!botTyping", "last.from_bot",
		"if (!items.length || !wanted(seq)) return;", "onclick: () => submit(text)",
		"input.addEventListener('input', clearSuggestions)", "if (!added) clearSuggestions();",
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
