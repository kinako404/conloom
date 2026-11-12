package napcat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testBotID = "bot_main"

func loadEvent(t *testing.T, name string) OBEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := ParseEvent(raw)
	if err != nil {
		t.Fatalf("ParseEvent(%s): %v", name, err)
	}
	return ev
}

func standard(t *testing.T, name string) (conloomapi.Event, string) {
	t.Helper()
	got, key, err := ToStandard(loadEvent(t, name), testBotID)
	if err != nil {
		t.Fatalf("ToStandard(%s): %v", name, err)
	}
	return got, key
}

func TestToStandard_GroupAtBot(t *testing.T) {
	got, key := standard(t, "group_at_bot.json")

	if key != "napcat:30001:7001" {
		t.Errorf("idempotency key = %q", key)
	}
	if got.SchemaVersion != conloomapi.SchemaVersion || got.EventType != conloomapi.EventTypeMessageCreated {
		t.Errorf("schema/event type = %q/%q", got.SchemaVersion, got.EventType)
	}
	if got.EventID != "evt_30001_7001" || got.BotInstanceID != testBotID || got.Platform != "qq" {
		t.Errorf("event id/bot/platform = %q/%q/%q", got.EventID, got.BotInstanceID, got.Platform)
	}
	if !got.OccurredAt.Equal(time.Unix(1762905600, 0)) {
		t.Errorf("occurred_at = %v", got.OccurredAt)
	}
	wantConv := conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_10001", PlatformConversationID: "10001"}
	if got.Conversation != wantConv {
		t.Errorf("conversation = %+v", got.Conversation)
	}
	wantActor := conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "小明", Roles: []string{"member"}}
	if !reflect.DeepEqual(got.Actor, wantActor) {
		t.Errorf("actor = %+v", got.Actor)
	}
	if got.Message.ID != "msg_7001" || got.Message.PlatformMessageID != "7001" {
		t.Errorf("message id = %q/%q", got.Message.ID, got.Message.PlatformMessageID)
	}
	if got.Message.Text != "你好" {
		t.Errorf("text = %q", got.Message.Text)
	}
	wantMentions := []conloomapi.Mention{{Type: "bot", ID: testBotID}}
	if !reflect.DeepEqual(got.Message.Mentions, wantMentions) {
		t.Errorf("mentions = %+v", got.Message.Mentions)
	}
	if got.Message.ReplyTo != nil {
		t.Errorf("reply_to = %v", *got.Message.ReplyTo)
	}
	if got.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true}) {
		t.Errorf("trigger = %+v", got.Trigger)
	}
}

func TestToStandard_AtAllIsNotBotMention(t *testing.T) {
	got, _ := standard(t, "group_at_all.json")

	wantMentions := []conloomapi.Mention{{Type: "all", ID: "all"}}
	if !reflect.DeepEqual(got.Message.Mentions, wantMentions) {
		t.Errorf("mentions = %+v", got.Message.Mentions)
	}
	if got.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerObserve, RequestedResponse: false}) {
		t.Errorf("trigger = %+v", got.Trigger)
	}
	if got.Message.Text != "开会了" {
		t.Errorf("text = %q", got.Message.Text)
	}
	if !reflect.DeepEqual(got.Actor.Roles, []string{"admin"}) {
		t.Errorf("roles = %v", got.Actor.Roles)
	}
}

func TestToStandard_ReplyAndImage(t *testing.T) {
	got, _ := standard(t, "group_reply_image.json")

	if got.Message.ReplyTo == nil || *got.Message.ReplyTo != "msg_555" {
		t.Fatalf("reply_to = %v", got.Message.ReplyTo)
	}
	wantAtt := []conloomapi.Attachment{{Type: "image", URL: "https://example.com/abc.jpg", Name: "abc.jpg"}}
	if !reflect.DeepEqual(got.Message.Attachments, wantAtt) {
		t.Errorf("attachments = %+v", got.Message.Attachments)
	}
	if got.Message.Text != "看这个[表情]" {
		t.Errorf("text = %q", got.Message.Text)
	}
	if got.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerReply, RequestedResponse: true}) {
		t.Errorf("trigger = %+v", got.Trigger)
	}
}

func TestToStandard_CardOverNickname(t *testing.T) {
	got, _ := standard(t, "group_card.json")

	if got.Actor.DisplayName != "群名片" {
		t.Errorf("display name = %q", got.Actor.DisplayName)
	}
	if got.Actor.ID != "user_20002" || !reflect.DeepEqual(got.Actor.Roles, []string{"owner"}) {
		t.Errorf("actor = %+v", got.Actor)
	}
	if got.Trigger.Type != conloomapi.TriggerObserve || got.Trigger.RequestedResponse {
		t.Errorf("trigger = %+v", got.Trigger)
	}
}

func TestToStandard_PrivateMessage(t *testing.T) {
	got, key := standard(t, "private.json")

	wantConv := conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: "private_20001", PlatformConversationID: "20001"}
	if got.Conversation != wantConv {
		t.Errorf("conversation = %+v", got.Conversation)
	}
	if got.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerPrivate, RequestedResponse: true}) {
		t.Errorf("trigger = %+v", got.Trigger)
	}
	if !reflect.DeepEqual(got.Actor.Roles, []string{"member"}) {
		t.Errorf("roles = %v", got.Actor.Roles)
	}
	if got.Actor.DisplayName != "小明" || got.Message.Text != "在吗" {
		t.Errorf("display name/text = %q/%q", got.Actor.DisplayName, got.Message.Text)
	}
	if key != "napcat:30001:7005" {
		t.Errorf("key = %q", key)
	}
}

func TestToStandard_LargeIDsPreserved(t *testing.T) {
	got, key := standard(t, "large_ids.json")

	if got.Message.PlatformMessageID != "9007199254740993" || got.Message.ID != "msg_9007199254740993" {
		t.Errorf("message id = %q/%q", got.Message.ID, got.Message.PlatformMessageID)
	}
	if got.EventID != "evt_30001_9007199254740993" || key != "napcat:30001:9007199254740993" {
		t.Errorf("event id/key = %q/%q", got.EventID, key)
	}
	if got.Message.ReplyTo == nil || *got.Message.ReplyTo != "msg_9007199254740995" {
		t.Errorf("reply_to = %v", got.Message.ReplyTo)
	}
	if got.Trigger.Type != conloomapi.TriggerReply {
		t.Errorf("trigger = %+v", got.Trigger)
	}
}

func TestToStandard_CommandTrigger(t *testing.T) {
	ev := loadEvent(t, "group_card.json")
	ev.Message = json.RawMessage(`[{"type":"text","data":{"text":"  /status"}}]`)
	got, _, err := ToStandard(ev, testBotID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != (conloomapi.Trigger{Type: conloomapi.TriggerCommand, RequestedResponse: true}) {
		t.Errorf("trigger = %+v", got.Trigger)
	}
	if got.Message.Text != "/status" {
		t.Errorf("text = %q", got.Message.Text)
	}
}

func TestToStandard_RejectsNonMessage(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"time":1,"self_id":30001,"post_type":"notice","notice_type":"group_increase"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ToStandard(ev, testBotID); err == nil {
		t.Error("expected error for notice event")
	}
}

func TestParseSegments_CQStringEqualsArray(t *testing.T) {
	fromArray, keyA := standard(t, "group_mixed_array.json")
	fromCQ, keyC := standard(t, "group_mixed_cq.json")
	fromArray.Extensions = nil
	fromCQ.Extensions = nil

	if keyA != keyC {
		t.Errorf("keys differ: %q vs %q", keyA, keyC)
	}
	if !reflect.DeepEqual(fromArray, fromCQ) {
		t.Errorf("array and CQ results differ:\narray: %+v\ncq:    %+v", fromArray, fromCQ)
	}

	if fromArray.Message.Text != "看[这]个, a&b [语音][视频][文件][表情]" {
		t.Errorf("text = %q", fromArray.Message.Text)
	}
	wantMentions := []conloomapi.Mention{{Type: "bot", ID: testBotID}, {Type: "user", ID: "user_20002"}}
	if !reflect.DeepEqual(fromArray.Message.Mentions, wantMentions) {
		t.Errorf("mentions = %+v", fromArray.Message.Mentions)
	}
	wantAtt := []conloomapi.Attachment{{Type: "image", URL: "https://example.com/x.png?a=1&b=2", Name: "x,y.png"}}
	if !reflect.DeepEqual(fromArray.Message.Attachments, wantAtt) {
		t.Errorf("attachments = %+v", fromArray.Message.Attachments)
	}
	if fromArray.Message.ReplyTo == nil || *fromArray.Message.ReplyTo != "msg_556" {
		t.Errorf("reply_to = %v", fromArray.Message.ReplyTo)
	}
	if fromArray.Trigger.Type != conloomapi.TriggerMention || fromArray.Actor.DisplayName != "阿明" {
		t.Errorf("trigger/display = %+v/%q", fromArray.Trigger, fromArray.Actor.DisplayName)
	}
}

func TestParseSegments_CQUnescape(t *testing.T) {
	segs, err := ParseSegments(json.RawMessage(`"&#91;x&#93; &amp;#91; [CQ:face,id=1]tail"`))
	if err != nil {
		t.Fatal(err)
	}
	want := []OBSegment{
		{Type: "text", Data: map[string]any{"text": "[x] &#91; "}},
		{Type: "face", Data: map[string]any{"id": "1"}},
		{Type: "text", Data: map[string]any{"text": "tail"}},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Errorf("segments = %#v", segs)
	}
}

func TestParseSegments_UnterminatedCQIsText(t *testing.T) {
	segs, err := ParseSegments(json.RawMessage(`"a[CQ:at,qq=1"`))
	if err != nil {
		t.Fatal(err)
	}
	want := []OBSegment{{Type: "text", Data: map[string]any{"text": "a[CQ:at,qq=1"}}}
	if !reflect.DeepEqual(segs, want) {
		t.Errorf("segments = %#v", segs)
	}
}

func messageSegments(t *testing.T, params map[string]any) []map[string]any {
	t.Helper()
	msg, ok := params["message"].([]map[string]any)
	if !ok {
		t.Fatalf("message param has type %T", params["message"])
	}
	return msg
}

func TestSendRequest_GroupWithReply(t *testing.T) {
	text := "收到，马上看"
	action, params, err := SendRequest(conloomapi.Action{
		ActionID:                 "act_1",
		Type:                     conloomapi.ActionMessageSend,
		ConversationType:         conloomapi.ConvGroup,
		PlatformConversationID:   "10001",
		ReplyToPlatformMessageID: "7001",
		Content:                  conloomapi.ActionContent{Text: text},
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != "send_group_msg" {
		t.Errorf("action = %q", action)
	}
	if params["group_id"] != int64(10001) {
		t.Errorf("group_id = %#v", params["group_id"])
	}
	msg := messageSegments(t, params)
	if len(msg) != 2 {
		t.Fatalf("message = %#v", msg)
	}
	if msg[0]["type"] != "reply" || !reflect.DeepEqual(msg[0]["data"], map[string]any{"id": "7001"}) {
		t.Errorf("message[0] = %#v", msg[0])
	}
	if msg[1]["type"] != "text" || !reflect.DeepEqual(msg[1]["data"], map[string]any{"text": text}) {
		t.Errorf("message[1] = %#v", msg[1])
	}
}

func TestSendRequest_PrivateWithoutReply(t *testing.T) {
	action, params, err := SendRequest(conloomapi.Action{
		Type:                   conloomapi.ActionMessageSend,
		ConversationType:       conloomapi.ConvPrivate,
		PlatformConversationID: "20001",
		Content:                conloomapi.ActionContent{Text: "你好"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != "send_private_msg" || params["user_id"] != int64(20001) {
		t.Errorf("action/user_id = %q/%#v", action, params["user_id"])
	}
	if _, ok := params["group_id"]; ok {
		t.Error("private request must not carry group_id")
	}
	msg := messageSegments(t, params)
	if len(msg) != 1 || msg[0]["type"] != "text" {
		t.Errorf("message = %#v", msg)
	}
}

func TestSendRequest_TextIsNeverCQParsed(t *testing.T) {
	_, params, err := SendRequest(conloomapi.Action{
		Type:                   conloomapi.ActionMessageSend,
		ConversationType:       conloomapi.ConvGroup,
		PlatformConversationID: "10001",
		Content:                conloomapi.ActionContent{Text: "[CQ:at,qq=all]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"type": "text", "data": map[string]any{"text": "[CQ:at,qq=all]"}}}
	if got := messageSegments(t, params); !reflect.DeepEqual(got, want) {
		t.Errorf("message = %#v", got)
	}
}

func TestSendRequest_Unsupported(t *testing.T) {
	_, _, err := SendRequest(conloomapi.Action{Type: "typing.start", ConversationType: conloomapi.ConvGroup, PlatformConversationID: "10001"})
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}

func TestSendRequest_BadTarget(t *testing.T) {
	for _, a := range []conloomapi.Action{
		{Type: conloomapi.ActionMessageSend, ConversationType: conloomapi.ConvGroup, PlatformConversationID: "abc"},
		{Type: conloomapi.ActionMessageSend, ConversationType: "channel", PlatformConversationID: "10001"},
	} {
		if _, _, err := SendRequest(a); err == nil || errors.Is(err, ErrUnsupportedAction) {
			t.Errorf("SendRequest(%+v) err = %v, want target error", a, err)
		}
	}
}
