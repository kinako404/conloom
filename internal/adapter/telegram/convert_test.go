package telegram

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testBotID = "bot_tg"

var testSelf = botUser{ID: fakeBotID, IsBot: true, FirstName: "织语", Username: fakeBotUsername}

func decodeUpdate(t *testing.T, raw map[string]any) update {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var u update
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func convert(t *testing.T, raw map[string]any) (conloomapi.Event, string, error) {
	t.Helper()
	return toStandard(decodeUpdate(t, raw), testSelf, testBotID)
}

func TestToStandard_PrivateText(t *testing.T) {
	ev, key, err := convert(t, textUpdate(9001, 11, "在吗？"))
	if err != nil {
		t.Fatal(err)
	}
	if key != "telegram:30001:9001" {
		t.Errorf("key = %q", key)
	}
	want := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_30001_9001",
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: testBotID,
		Platform:      "telegram",
		OccurredAt:    time.Unix(1794470400, 0).UTC(),
		Conversation:  conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: "private_20001", PlatformConversationID: "20001"},
		Actor:         conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "小明", Roles: []string{"member"}},
		Trigger:       conloomapi.Trigger{Type: conloomapi.TriggerPrivate, RequestedResponse: true},
		Message: conloomapi.Message{
			ID: "msg_20001_11", PlatformMessageID: "20001_11", Text: "在吗？",
			Mentions: []conloomapi.Mention{}, Attachments: []conloomapi.Attachment{},
		},
		Extensions: map[string]any{"telegram": map[string]any{"update_id": int64(9001)}},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("event =\n%+v\nwant\n%+v", ev, want)
	}
}

func TestToStandard_ReplyUsesChatScopedID(t *testing.T) {
	raw := tgUpdate(9002, privateMessage(13, map[string]any{
		"text":             "真的吗",
		"reply_to_message": map[string]any{"message_id": 12, "chat": map[string]any{"id": userXM, "type": "private"}, "date": 1794470300, "text": "我在"},
	}))
	ev, _, err := convert(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.Message.ReplyTo; got == nil || *got != "msg_20001_12" {
		t.Errorf("reply_to = %s, want msg_20001_12", reflect.ValueOf(got).Elem())
	}
	if ev.Trigger.Type != conloomapi.TriggerPrivate {
		t.Errorf("trigger = %q, want private", ev.Trigger.Type)
	}
}

func TestToStandard_DisplayName(t *testing.T) {
	cases := []struct {
		from map[string]any
		want string
	}{
		{tgUser(userXM, "Ming", "Li", "xiaoming"), "Ming Li"},
		{tgUser(userXM, "  小明 ", "", "xiaoming"), "小明"},
		{tgUser(userXM, "", "", "xiaoming"), "xiaoming"},
		{tgUser(userXM, " ", " ", "xiaoming"), "xiaoming"},
	}
	for _, c := range cases {
		msg := privateMessage(11, map[string]any{"text": "hi", "from": c.from})
		ev, _, err := convert(t, tgUpdate(9003, msg))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Actor.DisplayName != c.want {
			t.Errorf("from %v: display name = %q, want %q", c.from, ev.Actor.DisplayName, c.want)
		}
	}
}

func TestToStandard_Media(t *testing.T) {
	photo := []any{map[string]any{"file_id": "p1", "width": 90, "height": 90}}
	file := map[string]any{"file_id": "f1"}
	cases := []struct {
		name   string
		fields map[string]any
		want   string
	}{
		{"photo with caption", map[string]any{"photo": photo, "caption": "看这个"}, "[图片] 看这个"},
		{"photo", map[string]any{"photo": photo}, "[图片]"},
		{"sticker", map[string]any{"sticker": map[string]any{"file_id": "s1", "emoji": "😀"}}, "[表情]"},
		{"voice", map[string]any{"voice": file}, "[语音]"},
		{"audio", map[string]any{"audio": file, "caption": "歌"}, "[语音] 歌"},
		{"video", map[string]any{"video": file}, "[视频]"},
		{"video note", map[string]any{"video_note": file}, "[视频]"},
		{"animation", map[string]any{"animation": file, "document": file}, "[视频]"},
		{"document", map[string]any{"document": file, "caption": "报告"}, "[文件] 报告"},
	}
	for _, c := range cases {
		ev, _, err := convert(t, tgUpdate(9004, privateMessage(11, c.fields)))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if ev.Message.Text != c.want {
			t.Errorf("%s: text = %q, want %q", c.name, ev.Message.Text, c.want)
		}
		if len(ev.Message.Attachments) != 0 {
			t.Errorf("%s: attachments = %v, want none (the placeholder is in the text)", c.name, ev.Message.Attachments)
		}
	}
}

func TestToStandard_Commands(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/start", "你好"},
		{"/start ref_abc", "你好"},
		{"/start@zhiyu_test_bot", "你好"},
		{"/help@zhiyu_test_bot", "/help"},
		{"/help@ZHIYU_TEST_BOT now", "/help now"},
		{"/记住@zhiyu_test_bot 我喜欢猫", "/记住 我喜欢猫"},
		{"/记住 我喜欢猫", "/记住 我喜欢猫"},
		{"/help@other_bot", "/help@other_bot"},
		{"/pause\n马上", "/pause\n马上"},
		{"说 /start", "说 /start"},
	}
	for _, c := range cases {
		ev, _, err := convert(t, textUpdate(9005, 11, c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if ev.Message.Text != c.want {
			t.Errorf("%q: text = %q, want %q", c.in, ev.Message.Text, c.want)
		}
	}
}

func TestToStandard_Skips(t *testing.T) {
	fromBot := privateMessage(11, map[string]any{"text": "hi", "from": map[string]any{"id": 30009, "is_bot": true, "first_name": "bot"}})
	noFrom := privateMessage(11, map[string]any{"text": "hi"})
	delete(noFrom, "from")
	cases := map[string]map[string]any{
		"channel":       channelUpdate(9006, 11, "频道公告"),
		"no message":    {"update_id": 9007, "edited_message": privateMessage(11, map[string]any{"text": "改了"})},
		"from a bot":    tgUpdate(9008, fromBot),
		"no sender":     tgUpdate(9009, noFrom),
		"location only": tgUpdate(9010, privateMessage(11, map[string]any{"location": map[string]any{"latitude": 1.0, "longitude": 2.0}})),
		"blank text":    textUpdate(9011, 11, "  "),
	}
	for name, raw := range cases {
		if ev, _, err := convert(t, raw); err == nil {
			t.Errorf("%s: converted to %+v, want skipped", name, ev)
		}
	}
}
