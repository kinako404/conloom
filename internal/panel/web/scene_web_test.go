package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// The autonomy tab starts with a 场景 section: the scene, the fallback note,
// and switches to online and to in person with a place the client checks.
func TestAutonomyTab_SceneSection(t *testing.T) {
	all := assets(t, ".js")
	js := all["views/autonomy.js"]
	for _, want := range []string{
		"'场景'", "'超过 3 小时没说话，已回到线上'", "'autonomy', 'scene'",
		"mode: 'in_person'", "mode: 'online'", "scene.enabled", "scene.idle_online",
		"sceneLabel(", "SCENE_PLACE_MAX", "'地点最多 16 个字'",
		"'bot 没有启用 scene 技能，不区分线上和面对面。'", "'已切到线上'", "'已切到面对面'",
		"'地点（可选，最多 16 字）'", "maxlength: SCENE_PLACE_MAX", "[...place].length > SCENE_PLACE_MAX",
		"'模型用 set_scene 切换；面对面时对方超过 3 小时没说话，会自动回到线上。'",
		"['现在', sceneLabel(scene)], ['开始', fmtTime(scene.since_at)]",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/autonomy.js lacks %q", want)
		}
	}
	m := regexp.MustCompile(`(?s)export async function autonomyTab\(.*?\n\}`).FindString(js)
	if m == "" {
		t.Fatal("views/autonomy.js has no autonomyTab")
	}
	scene, status := strings.Index(m, "sceneSection("), strings.Index(m, "statusSection(")
	if scene < 0 || status < 0 || scene > status {
		t.Errorf("autonomyTab must render sceneSection( before statusSection(:\n%s", m)
	}

	format := all["lib/format.js"]
	for _, want := range []string{"export function sceneLabel(scene)", "`面对面 · ${", "'面对面'", "'线上'"} {
		if !strings.Contains(format, want) {
			t.Errorf("lib/format.js lacks %q", want)
		}
	}
	limit := regexp.MustCompile(`export const SCENE_PLACE_MAX = (\d+);`).FindStringSubmatch(format)
	if limit == nil {
		t.Fatal("lib/format.js has no SCENE_PLACE_MAX")
	}
	if want := strconv.Itoa(conloomapi.ScenePlaceMaxRunes); limit[1] != want {
		t.Errorf("SCENE_PLACE_MAX = %s, want conloomapi.ScenePlaceMaxRunes %s", limit[1], want)
	}
}

// The #/im chat header shows the scene under the bot's name while the chat is
// in person, unless 正在输入 shows there; GET chat's scene updates it on every
// poll.
func TestChatPane_SceneLabel(t *testing.T) {
	files := assets(t, ".js", ".css")
	js := files["views/chat.js"]
	for _, want := range []string{"res.scene", "sceneLabel(", "tg-scene", "'in_person'"} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	toggle := regexp.MustCompile(`\.hidden = [^;\n]*botTyping[^;\n]*;`).FindString(js)
	if toggle == "" || !strings.Contains(toggle, "'in_person'") {
		t.Errorf("views/chat.js does not hide the scene label while the bot is typing or not in person (%q)", toggle)
	}
	if !regexp.MustCompile(`\.chat-app \.tg-scene\s*\{[^}]*color:\s*var\(--muted\)`).MatchString(files["style.css"]) {
		t.Error("style.css does not mute .chat-app .tg-scene")
	}
}

// On the #/im page, a short action in full-width brackets in a message is
// muted; the classic page shows messages as they are.
func TestChatPane_ActionBrackets(t *testing.T) {
	files := assets(t, ".js", ".css")
	format := files["lib/format.js"]
	for _, want := range []string{"/（[^（）\\n]*）/g", "export function actionParts"} {
		if !strings.Contains(format, want) {
			t.Errorf("lib/format.js lacks %q", want)
		}
	}
	js := files["views/chat.js"]
	for _, want := range []string{"actionParts(m.text)", "class: 'tg-action'", "h('span', { class: 'tg-text' }"} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	if !regexp.MustCompile(`\.chat-app \.tg-action\s*\{[^}]*color:\s*var\(--muted\)`).MatchString(files["style.css"]) {
		t.Error("style.css does not mute .chat-app .tg-action")
	}
	if strings.Contains(files["views/chatclassic.js"], "tg-action") {
		t.Error("views/chatclassic.js uses tg-action")
	}
}
