package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// The chat's composer attaches up to four PNG or JPEG images by the 📎
// button, a paste or a drop, previews them with a 移除图片 button each,
// uploads them one at a time and sends their ids with the text; its limits
// are the server's.
func TestChatPane_ImageComposer(t *testing.T) {
	js := assets(t, ".js")["views/chat.js"]
	for _, c := range []struct {
		name string
		want int
	}{
		{"MAX_IMAGES", conloomapi.MaxPanelMessageImages},
		{"MAX_IMAGE_MIB", imaging.MaxBytes >> 20},
	} {
		m := regexp.MustCompile(`const ` + c.name + ` = (\d+);`).FindStringSubmatch(js)
		if m == nil {
			t.Errorf("views/chat.js has no %s", c.name)
			continue
		}
		if want := strconv.Itoa(c.want); m[1] != want {
			t.Errorf("%s = %s, want %s", c.name, m[1], want)
		}
	}
	for _, want := range []string{
		"IMAGE_TYPES = ['image/png', 'image/jpeg']",
		"accept: 'image/png,image/jpeg'", "multiple: true",
		"'paste'", "'dragover'", "'drop'", "'tg-drop'", "class: 'tg-attach'", "class: 'tg-attachments'",
		"upload(convPath(key, 'images'), ", "images: ids",
		"'发送图片'", "'移除图片'", "'上传中…'",
		"'一条消息最多 4 张图片'", "'只支持 PNG 和 JPEG 图片'", "`图片不能超过 ${MAX_IMAGE_MIB} MiB`",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	// A text-only message is sent as before.
	if !strings.Contains(js, "{ text }") {
		t.Error("views/chat.js no longer sends a text-only message as { text }")
	}
}

// A message's images show in its bubble from the reply copy and open the view
// copy in a viewer; the classic page shows them too, with its own classes.
func TestChatPane_ImageBubbles(t *testing.T) {
	files := assets(t, ".js")
	js := files["views/chat.js"]
	for _, want := range []string{
		"export function imageStrip(key, m, signal, look = TG_IMAGES) {",
		"chatImageURL(key, id, 'reply')", "chatImageURL(key, id, 'view')",
		"const TG_IMAGES = { root: 'chat-app', strip: 'tg-images', image: 'tg-image', viewer: 'tg-viewer' };",
		"class: look.strip", "class: look.image", "alt: '图片'", "loading: 'lazy'",
		"class: look.viewer", "role: 'dialog'", "'aria-label': '查看图片'", "'Escape'",
		"m.text ? h('span', { class: 'tg-text' }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/chat.js lacks %q", want)
		}
	}
	if !strings.Contains(files["lib/api.js"], "export function chatImageURL(key, id, size) {") {
		t.Error("lib/api.js has no chatImageURL(key, id, size)")
	}
	classic := files["views/chatclassic.js"]
	if !regexp.MustCompile(`import \{[^}]*\bimageStrip\b[^}]*\} from './chat.js';`).MatchString(classic) {
		t.Error("views/chatclassic.js does not import imageStrip from ./chat.js")
	}
	for _, want := range []string{
		"const IMAGES = { root: 'chat-classic', strip: 'chat-images', image: 'chat-image', viewer: 'chat-viewer' };",
		"imageStrip(key, m, signal, IMAGES)",
	} {
		if !strings.Contains(classic, want) {
			t.Errorf("views/chatclassic.js lacks %q", want)
		}
	}
}

// The chat list shows [图片] for a message with images, before its text.
func TestChatsList_ImagePreview(t *testing.T) {
	files := assets(t, ".js")
	format := files["lib/format.js"]
	if !strings.Contains(format, "export function previewText(m) {") || !strings.Contains(format, "'[图片]'") {
		t.Error("lib/format.js has no previewText giving [图片]")
	}
	chats := files["views/chats.js"]
	if n := strings.Count(chats, "previewText(c.last)"); n < 2 {
		t.Errorf("views/chats.js uses previewText(c.last) %d times, want the preview and the search", n)
	}
	// A message that arrives in the chat open keeps its images as the chat's
	// last, as GET chats gives them.
	if !strings.Contains(chats, "images: m.images && m.images.length ? m.images : undefined,") {
		t.Error("views/chats.js lastOf drops a message's images")
	}
}

// Each chat page styles the images under its own root (see
// TestChatPages_StylesScoped): the composer, the drop outline and the
// viewer's fade on the Telegram-style page, the images and the viewer on
// both.
func TestChatImages_StylesScoped(t *testing.T) {
	css := assets(t, ".css")["style.css"]
	for _, want := range []string{
		".chat-app .tg-attach {", ".chat-app .tg-attachments {", ".chat-app .tg-conv.tg-drop",
		".chat-app .tg-images,", ".chat-app .tg-image,", ".chat-app .tg-viewer,",
		".chat-classic .chat-images {", ".chat-classic .chat-image {", ".chat-classic .chat-viewer {",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
	for _, c := range []struct{ name, re string }{
		{"thumbnails cropped to fit", `\.chat-classic \.chat-image\s*\{[^}]*object-fit:\s*cover;`},
		{"one image at most 240×240", `\.chat-classic \.chat-images\[data-count="1"\] \.chat-image\.loaded\s*\{[^}]*max-width:\s*240px;[^}]*max-height:\s*240px;`},
		{"two to a row", `\.chat-classic \.chat-images\s*\{[^}]*grid-template-columns:\s*repeat\(2,`},
		{"a viewer over the page", `\.chat-classic \.chat-viewer\s*\{[^}]*position:\s*fixed;`},
		{"the view copy at most 95% of the window", `\.chat-classic \.chat-viewer img\s*\{[^}]*max-width:\s*95vw;[^}]*max-height:\s*95vh;`},
	} {
		if !regexp.MustCompile(c.re).MatchString(css) {
			t.Errorf("style.css lacks %s (%s)", c.name, c.re)
		}
	}
}
