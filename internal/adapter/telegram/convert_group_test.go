package telegram

import (
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestToStandard_GroupMentioningBot(t *testing.T) {
	raw := tgUpdate(9100, groupMessage(11, map[string]any{
		"text": "@zhiyu_test_bot 在吗？", "entities": []any{tgEntity("mention", 0, 15)},
	}))
	ev, key, err := convert(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if key != "telegram:30001:9100" {
		t.Errorf("key = %q", key)
	}
	want := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_30001_9100",
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: testBotID,
		Platform:      "telegram",
		OccurredAt:    time.Unix(1794470400, 0).UTC(),
		Conversation: conloomapi.Conversation{
			Type: conloomapi.ConvGroup, ID: "group_-1001234567890", PlatformConversationID: "-1001234567890",
		},
		Actor:   conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "小明", Roles: []string{"member"}},
		Trigger: conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true},
		Message: conloomapi.Message{
			ID: "msg_-1001234567890_11", PlatformMessageID: "-1001234567890_11", Text: "在吗？",
			Mentions:    []conloomapi.Mention{{Type: "bot", ID: testBotID}},
			Attachments: []conloomapi.Attachment{},
		},
		Extensions: map[string]any{"telegram": map[string]any{"update_id": int64(9100)}},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("event =\n%+v\nwant\n%+v", ev, want)
	}

	basic := tgUpdate(9101, groupMessage(12, map[string]any{"text": "大家好"}))
	basic["message"].(map[string]any)["chat"] = map[string]any{"id": -4001234, "type": "group", "title": "小群"}
	ev, _, err = convert(t, basic)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Conversation != (conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_-4001234", PlatformConversationID: "-4001234"}) ||
		ev.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerObserve}) || ev.Message.ID != "msg_-4001234_12" {
		t.Errorf("basic group event = %+v", ev)
	}
}

func TestToStandard_GroupMentions(t *testing.T) {
	xiaohong := map[string]any{"id": 20002, "is_bot": false, "first_name": "小红"}
	textMention := tgEntity("text_mention", 0, 2)
	textMention["user"] = xiaohong
	cases := []struct {
		name     string
		text     string
		entities []any
		wantText string
		want     []conloomapi.Mention
		trigger  string
	}{
		{"other by username", "@xiaohong 你看", []any{tgEntity("mention", 0, 9)}, "@xiaohong 你看",
			[]conloomapi.Mention{{Type: "user", ID: "username_xiaohong", Name: "xiaohong"}}, conloomapi.TriggerObserve},
		{"other without username", "小红 你看", []any{textMention}, "小红 你看",
			[]conloomapi.Mention{{Type: "user", ID: "user_20002", Name: "小红"}}, conloomapi.TriggerObserve},
		{"bot after an emoji", "😀 @zhiyu_test_bot 在吗", []any{tgEntity("mention", 3, 15)}, "😀 在吗",
			[]conloomapi.Mention{{Type: "bot", ID: testBotID}}, conloomapi.TriggerMention},
		{"bot in another case", "@ZHIYU_TEST_BOT 在吗", []any{tgEntity("mention", 0, 15)}, "在吗",
			[]conloomapi.Mention{{Type: "bot", ID: testBotID}}, conloomapi.TriggerMention},
		{"bot only", "@zhiyu_test_bot", []any{tgEntity("mention", 0, 15)}, "",
			[]conloomapi.Mention{{Type: "bot", ID: testBotID}}, conloomapi.TriggerMention},
		{"bot and other", "@xiaohong 和 @zhiyu_test_bot 都来", []any{tgEntity("mention", 0, 9), tgEntity("mention", 12, 15)}, "@xiaohong 和 都来",
			[]conloomapi.Mention{{Type: "user", ID: "username_xiaohong", Name: "xiaohong"}, {Type: "bot", ID: testBotID}}, conloomapi.TriggerMention},
		{"no entities", "@zhiyu_test_bot 在吗", nil, "@zhiyu_test_bot 在吗", []conloomapi.Mention{}, conloomapi.TriggerObserve},
		{"entity out of range", "在吗", []any{tgEntity("mention", 1, 15)}, "在吗", []conloomapi.Mention{}, conloomapi.TriggerObserve},
	}
	for _, c := range cases {
		fields := map[string]any{"text": c.text}
		if c.entities != nil {
			fields["entities"] = c.entities
		}
		ev, _, err := convert(t, tgUpdate(9102, groupMessage(11, fields)))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if ev.Message.Text != c.wantText || !reflect.DeepEqual(ev.Message.Mentions, c.want) || ev.Trigger.Type != c.trigger {
			t.Errorf("%s: text %q, mentions %+v, trigger %q; want %q, %+v, %q",
				c.name, ev.Message.Text, ev.Message.Mentions, ev.Trigger.Type, c.wantText, c.want, c.trigger)
		}
	}
}

func TestToStandard_GroupCaptionMention(t *testing.T) {
	raw := tgUpdate(9103, groupMessage(11, map[string]any{
		"photo":            []any{map[string]any{"file_id": "p1"}},
		"caption":          "@zhiyu_test_bot 看这个",
		"caption_entities": []any{tgEntity("mention", 0, 15)},
	}))
	ev, _, err := convert(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Message.Text != "[图片] 看这个" || len(ev.Message.Mentions) != 1 || ev.Message.Mentions[0].Type != "bot" {
		t.Errorf("text %q, mentions %+v; want the caption without the bot mention and a bot mention", ev.Message.Text, ev.Message.Mentions)
	}
}

func TestToStandard_GroupReplyAndCommands(t *testing.T) {
	reply := tgUpdate(9104, groupMessage(13, map[string]any{
		"text":             "真的吗",
		"reply_to_message": map[string]any{"message_id": 501, "chat": map[string]any{"id": testGroupID, "type": "supergroup"}, "date": 1794470300},
	}))
	ev, _, err := convert(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Message.ReplyTo == nil || *ev.Message.ReplyTo != "msg_-1001234567890_501" || ev.Trigger.Type != conloomapi.TriggerReply {
		t.Errorf("reply: reply_to %v, trigger %q; want msg_-1001234567890_501 and reply", ev.Message.ReplyTo, ev.Trigger.Type)
	}

	cases := []struct {
		in, want   string
		trigger    string
		command    bool
		botMention bool
	}{
		{"/pause@zhiyu_test_bot", "/pause", conloomapi.TriggerCommand, true, false},
		{"/PAUSE@Zhiyu_Test_Bot 一会儿", "/PAUSE 一会儿", conloomapi.TriggerCommand, true, false},
		{"/pause", "/pause", conloomapi.TriggerCommand, true, false},
		{"/pause@other_bot", "/pause@other_bot", conloomapi.TriggerObserve, false, false},
		{"/help@other_bot 怎么用", "/help@other_bot 怎么用", conloomapi.TriggerObserve, false, false},
		{"/start@zhiyu_test_bot", "你好", conloomapi.TriggerMention, false, true},
		{"/start@other_bot", "/start@other_bot", conloomapi.TriggerObserve, false, false},
	}
	for _, c := range cases {
		ev, _, err := convert(t, groupUpdate(9105, 11, c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if ev.Message.Text != c.want || ev.Trigger.Type != c.trigger {
			t.Errorf("%q: text %q, trigger %q; want %q, %q", c.in, ev.Message.Text, ev.Trigger.Type, c.want, c.trigger)
		}
		if _, _, ok := core.ParseCommand(ev.Message.Text, "/"); ok != c.command {
			t.Errorf("%q: the core parses %q as a command = %v, want %v", c.in, ev.Message.Text, ok, c.command)
		}
		hasBot := len(ev.Message.Mentions) == 1 && ev.Message.Mentions[0] == conloomapi.Mention{Type: "bot", ID: testBotID}
		if hasBot != c.botMention {
			t.Errorf("%q: mentions %+v, want a bot mention = %v", c.in, ev.Message.Mentions, c.botMention)
		}
	}
}

func TestNegativeChatIDRoundTrip(t *testing.T) {
	ev, _, err := convert(t, groupUpdate(9106, 45, "大家好"))
	if err != nil {
		t.Fatal(err)
	}
	act := conloomapi.Action{
		ActionID: "act_g", Type: conloomapi.ActionMessageSend,
		ConversationID: ev.Conversation.ID, ConversationType: ev.Conversation.Type,
		PlatformConversationID:   ev.Conversation.PlatformConversationID,
		ReplyToMessageID:         ev.Message.ID,
		ReplyToPlatformMessageID: ev.Message.PlatformMessageID,
		Content:                  conloomapi.ActionContent{Text: "收到"},
	}
	plan, err := buildSend(act)
	if err != nil {
		t.Fatal(err)
	}
	if plan.chatID != testGroupID || plan.replyTo != 45 {
		t.Errorf("plan = %+v, want chat %d quoting message 45", plan, int64(testGroupID))
	}
	if got := "msg_" + platformMessageID(plan.chatID, 46); got != "msg_-1001234567890_46" {
		t.Errorf("bot message ID = %q, want the form inbound replies use", got)
	}
}
