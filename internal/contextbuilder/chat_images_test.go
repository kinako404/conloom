package contextbuilder

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	chatURLA = "data:image/jpeg;base64,QQ=="
	chatURLB = "data:image/jpeg;base64,Qg=="
	chatURLC = "data:image/jpeg;base64,Qw=="
	chatURLD = "data:image/jpeg;base64,RA=="
	chatURLE = "data:image/jpeg;base64,RQ=="
	appURL   = "data:image/jpeg;base64,YXBw"
)

func imageAttachments(ids ...string) []conloomapi.Attachment {
	out := make([]conloomapi.Attachment, 0, len(ids))
	for _, id := range ids {
		out = append(out, conloomapi.Attachment{Type: "image", ID: id})
	}
	return out
}

func withImages(rec store.MessageRecord, ids ...string) store.MessageRecord {
	rec.Attachments = imageAttachments(ids...)
	return rec
}

func chatHistory() []store.MessageRecord {
	return []store.MessageRecord{userMsg("小明", "在吗", at(12, 0)), botMsg("在", at(12, 1))}
}

func lastMessage(out Output) openai.ChatMessage {
	return out.Messages[len(out.Messages)-1]
}

func assertNoImages(t *testing.T, msgs []openai.ChatMessage) {
	t.Helper()
	for i, m := range msgs {
		if len(m.Images) > 0 || len(m.Parts) > 0 {
			t.Errorf("message %d carries images: %+v", i, m)
		}
	}
}

func TestBuild_NoChatImagesUnchanged(t *testing.T) {
	in := imageInput()
	in.History = []store.MessageRecord{withImages(userMsg("小明", "看", at(12, 0)), ""), botMsg("嗯", at(12, 1))}
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "还有", at(12, 5)), "")}
	nilOut := Build(in)
	nilBase := BaseTokens(in)
	in.CurrentImages, in.ImageDescriptions, in.SeenEarlier = []ChatImage{}, map[string]string{}, map[string]bool{}
	emptyOut := Build(in)
	if !reflect.DeepEqual(nilOut, emptyOut) || BaseTokens(in) != nilBase {
		t.Errorf("empty CurrentImages/ImageDescriptions changed the output:\n%+v\n%+v", nilOut, emptyOut)
	}
	if !strings.Contains(nilOut.Messages[1].Content, "[12:00] 小明：看[图片]") {
		t.Errorf("history = %q, want a [图片] placeholder", nilOut.Messages[1].Content)
	}
	if !strings.Contains(lastMessage(nilOut).Content, "还有[图片]") {
		t.Errorf("current = %q, want a [图片] placeholder", lastMessage(nilOut).Content)
	}
	for i, m := range nilOut.Messages {
		if len(m.Parts) > 0 {
			t.Errorf("message %d has Parts: %+v", i, m)
		}
	}
}

func TestBuild_ChatImagesOnCurrentMessage(t *testing.T) {
	const tokA, tokB = 300, 500
	in := baseInput()
	in.History = chatHistory()
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "看这个", at(12, 5)), "img_a", "img_b")}
	plain := Build(in)
	plainBase := BaseTokens(in)
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: tokA}, {ID: "img_b", DataURL: chatURLB, Tokens: tokB}}
	out := Build(in)

	last := lastMessage(out)
	want := []openai.ChatPart{
		{Text: ChatImageLabel(1)}, {ImageURL: chatURLA},
		{Text: ChatImageLabel(2)}, {ImageURL: chatURLB},
		{Text: last.Content},
	}
	if !reflect.DeepEqual(last.Parts, want) {
		t.Errorf("Parts = %+v\nwant    %+v", last.Parts, want)
	}
	if !strings.Contains(last.Content, "看这个[图片1][图片2]") || strings.Contains(last.Content, "[图片]") {
		t.Errorf("Content = %q, want 看这个[图片1][图片2] and no bare [图片]", last.Content)
	}
	if last.Images != nil {
		t.Errorf("final message Images = %v, want nil", last.Images)
	}
	assertNoImages(t, out.Messages[:len(out.Messages)-1])
	if got := out.EstimatedTokens - plain.EstimatedTokens; got < tokA+tokB {
		t.Errorf("EstimatedTokens grew by %d, want at least %d", got, tokA+tokB)
	}
	if got := BaseTokens(in) - plainBase; got < tokA+tokB {
		t.Errorf("BaseTokens grew by %d, want at least %d", got, tokA+tokB)
	}
	if ChatImageLabel(2) != "【对方这条新消息里的第 2 张图片（即[图片2]），不是之前聊过的图片】" {
		t.Errorf("ChatImageLabel(2) = %q", ChatImageLabel(2))
	}
	for extra := range 60 {
		in.Budget = BaseTokens(in) + extra
		if o := Build(in); o.EstimatedTokens > in.Budget {
			t.Fatalf("EstimatedTokens = %d exceeds Budget %d", o.EstimatedTokens, in.Budget)
		}
	}
}

func TestBuild_ChatImagesWithAppearanceSameMessage(t *testing.T) {
	in := baseInput()
	in.Image = &ImageRef{DataURL: appURL, Tokens: 394}
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "看这个", at(12, 5)), "img_a")}
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: 300}}
	out := Build(in)

	users := slices.DeleteFunc(slices.Clone(out.Messages), func(m openai.ChatMessage) bool { return m.Role != "user" })
	if len(users) != 1 {
		t.Fatalf("user messages = %+v, want one", users)
	}
	m := users[0]
	want := []openai.ChatPart{
		{Text: AppearanceLabel}, {ImageURL: appURL},
		{Text: ChatImageLabel(1)}, {ImageURL: chatURLA},
		{Text: m.Content},
	}
	if !reflect.DeepEqual(m.Parts, want) {
		t.Errorf("Parts = %+v\nwant    %+v", m.Parts, want)
	}
	if m.Images != nil || strings.Contains(m.Content, AppearanceLabel) {
		t.Errorf("message = %+v, want no Images and no AppearanceLabel in Content", m)
	}
	if !strings.HasPrefix(m.Content, "【当前消息】") {
		t.Errorf("Content = %q, want it to start with the current block", m.Content)
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	var images int
	for i, p := range wire.Content {
		if p.Type != "image_url" {
			continue
		}
		images++
		label := map[string]string{appURL: AppearanceLabel, chatURLA: ChatImageLabel(1)}[p.ImageURL.URL]
		if i == 0 || wire.Content[i-1].Type != "text" || wire.Content[i-1].Text != label {
			t.Errorf("image %s at %d is not right after its label %q: %s", p.ImageURL.URL, i, label, raw)
		}
	}
	if images != 2 {
		t.Errorf("image_url parts = %d, want 2: %s", images, raw)
	}

	base := BaseTokens(in)
	for extra := range 60 {
		in.Budget = base + extra
		if o := Build(in); o.EstimatedTokens > in.Budget {
			t.Fatalf("EstimatedTokens = %d exceeds Budget %d", o.EstimatedTokens, in.Budget)
		}
	}
}

func TestBuild_ChatImagesWithAppearanceEarlierMessage(t *testing.T) {
	in := baseInput()
	in.Image = &ImageRef{DataURL: appURL, Tokens: 394}
	in.History = chatHistory()
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "看这个", at(12, 5)), "img_a")}
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: 300}}
	out := Build(in)

	first := out.Messages[1]
	if first.Role != "user" || first.Content != AppearanceLabel+"\n[12:00] 小明：在吗" || !reflect.DeepEqual(first.Images, []string{appURL}) || first.Parts != nil {
		t.Errorf("first user message = %+v, want today's appearance form", first)
	}
	last := lastMessage(out)
	want := []openai.ChatPart{{Text: ChatImageLabel(1)}, {ImageURL: chatURLA}, {Text: last.Content}}
	if !reflect.DeepEqual(last.Parts, want) || last.Images != nil {
		t.Errorf("final message = %+v\nwant Parts %+v", last, want)
	}
}

func TestBuild_ImageOnlyMessage(t *testing.T) {
	in := baseInput()
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "", at(12, 5)), "img_a")}
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: 300}}
	last := lastMessage(Build(in))
	if want := "【当前消息】发送者：小明\n[图片1]（对方这条只发了图片，没有文字）"; last.Content != want {
		t.Errorf("Content = %q, want %q", last.Content, want)
	}
	if imageOnlyNote != "（对方这条只发了图片，没有文字）" {
		t.Errorf("imageOnlyNote = %q", imageOnlyNote)
	}
	for extra := range 60 {
		in.Budget = BaseTokens(in) + extra
		if o := Build(in); o.EstimatedTokens > in.Budget {
			t.Fatalf("EstimatedTokens = %d exceeds Budget %d", o.EstimatedTokens, in.Budget)
		}
	}

	in.CurrentImages = []ChatImage{{ID: "img_a", Description: "一杯咖啡"}}
	if got := lastMessage(Build(in)).Content; strings.Contains(got, imageOnlyNote) {
		t.Errorf("Content with only a described image = %q, want no imageOnlyNote", got)
	}
}

func TestStripImageLabels(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"two labels", "左边[图片1]是猫，右边[图片2]是狗", "左边是猫，右边是狗"},
		{"spaces around", "[图片1] 是猫 [图片2] 是狗", "是猫 是狗"},
		{"own line", "[图片1]\n好可爱的猫", "好可爱的猫"},
		{"blank lines", "第一张\n\n[图片2]\n\n第二张", "第一张\n\n第二张"},
		{"echoed label", ChatImageLabel(1) + "\n这是一只猫", "这是一只猫"},
		{"echoed old label", "【对方发来的第 2 张图片，即消息里的[图片2]】这是一只狗", "这是一只狗"},
		{"seen placeholder", "还是那张" + imageSeenEarlierPlaceholder, "还是那张"},
		{"image-only note", "[图片1]" + imageOnlyNote + "\n好看", "好看"},
		{"only labels", "[图片1][图片2]", ""},
	} {
		if got := StripImageLabels(tc.in); got != tc.want {
			t.Errorf("%s: StripImageLabels(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	for _, keep := range []string{
		"哈哈[笑]（摸摸头）",
		"  [图片] 和 [图片：一只猫] 还有【重要】  ",
		"第[1]张\n\n\n很好看",
		"图片1 和 图片2",
	} {
		if got := StripImageLabels(keep); got != keep {
			t.Errorf("StripImageLabels(%q) = %q, want it unchanged", keep, got)
		}
	}
}

func TestBuild_BotHistoryDropsImageLabels(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{userMsg("小明", "看", at(12, 0)), botMsg("[图片1]是猫，[图片2]是狗", at(12, 1))}
	out := Build(in)
	if got := out.Messages[2].Content; got != "是猫，是狗" {
		t.Errorf("bot history = %q, want the labels dropped", got)
	}
}

func TestBuild_ChatImagesMergedOrder(t *testing.T) {
	in := baseInput()
	in.Current = []store.MessageRecord{
		withImages(userMsg("小明", "", at(12, 3)), "img_a", "img_b"),
		withImages(userMsg("小明", "还有", at(12, 4)), "img_c"),
		withImages(userMsg("小明", "", at(12, 5)), "img_d", "img_e"),
	}
	in.CurrentImages = []ChatImage{
		{ID: "img_b", DataURL: chatURLB, Tokens: 10},
		{ID: "img_c", DataURL: chatURLC, Tokens: 10},
		{ID: "img_d", DataURL: chatURLD, Tokens: 10},
		{ID: "img_e", DataURL: chatURLE, Tokens: 10},
	}
	in.ImageDescriptions = map[string]string{"img_a": "一只橘猫"}
	last := lastMessage(Build(in))
	if want := "【当前消息】发送者：小明\n[图片：一只橘猫][图片1]" + imageOnlyNote + "\n还有[图片2]\n[图片3][图片4]" + imageOnlyNote; last.Content != want {
		t.Errorf("Content = %q\nwant      %q", last.Content, want)
	}
	want := []openai.ChatPart{
		{Text: ChatImageLabel(1)}, {ImageURL: chatURLB},
		{Text: ChatImageLabel(2)}, {ImageURL: chatURLC},
		{Text: ChatImageLabel(3)}, {ImageURL: chatURLD},
		{Text: ChatImageLabel(4)}, {ImageURL: chatURLE},
		{Text: last.Content},
	}
	if !reflect.DeepEqual(last.Parts, want) {
		t.Errorf("Parts = %+v\nwant    %+v", last.Parts, want)
	}
}

func TestBuild_UnseenImages(t *testing.T) {
	in := baseInput()
	in.Directive = "（请回复）"
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "", at(12, 5)), "img_a", "img_b")}
	in.CurrentImages = []ChatImage{{ID: "img_a", Description: "桌上的\n一杯咖啡"}, {ID: "img_b"}}
	out := Build(in)
	last := lastMessage(out)
	want := "【当前消息】发送者：小明\n[图片：桌上的 一杯咖啡][图片（看不到内容）]\n" + imagesUnseenNote + "\n\n（请回复）"
	if last.Content != want {
		t.Errorf("Content = %q\nwant      %q", last.Content, want)
	}
	if strings.Count(last.Content, imagesUnseenNote) != 1 {
		t.Errorf("imagesUnseenNote appears %d times", strings.Count(last.Content, imagesUnseenNote))
	}
	assertNoImages(t, out.Messages)
	if imagesUnseenNote != "（标着“看不到内容”的图片你没有收到，不知道里面是什么，不要假装看到了。）" {
		t.Errorf("imagesUnseenNote = %q", imagesUnseenNote)
	}

	in.CurrentImages = []ChatImage{{ID: "img_a", Description: "一杯咖啡"}, {ID: "img_b", Description: "一只猫"}}
	if got := lastMessage(Build(in)).Content; strings.Contains(got, imagesUnseenNote) {
		t.Errorf("Content with every image described = %q, want no imagesUnseenNote", got)
	}
}

func TestBuild_HistoryImagesUseCachedDescriptions(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{withImages(userMsg("小明", "看", at(12, 0)), "img_x", "img_y"), botMsg("好看", at(12, 1))}
	in.ImageDescriptions = map[string]string{"img_x": "海边的\n日落"}
	out := Build(in)
	if want := "[12:00] 小明：看[图片：海边的 日落][图片]"; out.Messages[1].Content != want {
		t.Errorf("history = %q, want %q", out.Messages[1].Content, want)
	}
	assertNoImages(t, out.Messages)
}

func TestBuild_ImagesSeenEarlier(t *testing.T) {
	const seenEarlier = "[图片（你之前看过，细节以你当时的回复为准）]"
	if imageSeenEarlierPlaceholder != seenEarlier {
		t.Errorf("imageSeenEarlierPlaceholder = %q", imageSeenEarlierPlaceholder)
	}
	in := baseInput()
	in.History = []store.MessageRecord{withImages(userMsg("小明", "看", at(12, 0)), "img_x", "img_y", "img_z"), botMsg("好看", at(12, 1))}
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "还有", at(12, 5)), "img_old", "img_a")}
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: 10}}
	in.ImageDescriptions = map[string]string{"img_x": "海边的日落"}
	in.SeenEarlier = map[string]bool{"img_x": true, "img_y": true, "img_old": true, "img_a": true}
	out := Build(in)
	if want := "[12:00] 小明：看[图片：海边的日落]" + seenEarlier + "[图片]"; out.Messages[1].Content != want {
		t.Errorf("history = %q, want %q", out.Messages[1].Content, want)
	}
	last := lastMessage(out)
	if want := "还有" + seenEarlier + "[图片1]"; !strings.Contains(last.Content, want) || strings.Contains(last.Content, imagesUnseenNote) {
		t.Errorf("current = %q, want it to contain %q and no unseen note", last.Content, want)
	}
}

func TestWithoutImage_StripsEveryImage(t *testing.T) {
	in := baseInput()
	in.Image = &ImageRef{DataURL: appURL, Tokens: 394}
	in.History = chatHistory()
	in.Current = []store.MessageRecord{withImages(userMsg("小明", "看这个", at(12, 5)), "img_a", "img_b")}
	in.CurrentImages = []ChatImage{{ID: "img_a", DataURL: chatURLA, Tokens: 300}, {ID: "img_b", DataURL: chatURLB, Tokens: 300}}
	msgs := Build(in).Messages
	before := slices.Clone(msgs)
	for i := range before {
		before[i].Images = slices.Clone(msgs[i].Images)
		before[i].Parts = slices.Clone(msgs[i].Parts)
	}

	got := WithoutImage(msgs)
	assertNoImages(t, got)
	if strings.Contains(got[1].Content, AppearanceLabel) || got[1].Content != "[12:00] 小明：在吗" {
		t.Errorf("first user Content = %q, want it without AppearanceLabel", got[1].Content)
	}
	if got[len(got)-1].Content != msgs[len(msgs)-1].Content {
		t.Errorf("final Content = %q, want unchanged %q", got[len(got)-1].Content, msgs[len(msgs)-1].Content)
	}
	if !reflect.DeepEqual(msgs, before) {
		t.Errorf("WithoutImage changed its argument:\n%+v\nwant %+v", msgs, before)
	}

	in.History = nil
	same := Build(in).Messages
	got = WithoutImage(same)
	assertNoImages(t, got)
	if c := got[len(got)-1].Content; c != same[len(same)-1].Content || strings.Contains(c, AppearanceLabel) {
		t.Errorf("same-message Content = %q, want unchanged and without AppearanceLabel", c)
	}
}

func TestTranscript_ChatImagesStayPlaceholders(t *testing.T) {
	lines := Transcript(TranscriptInput{
		Messages: []store.MessageRecord{withImages(userMsg("小明", "", at(12, 0)), "img_a", "img_b")},
		Location: shanghai,
	})
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "：[图片][图片]") {
		t.Errorf("Transcript = %q, want the body [图片][图片]", lines)
	}
}
