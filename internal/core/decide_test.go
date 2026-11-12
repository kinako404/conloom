package core

import (
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func groupInput() DecideInput {
	return DecideInput{
		BotInstanceID:    "bot_main",
		SelfPlatformID:   "30001",
		ConversationType: conloomapi.ConvGroup,
		PlatformUserID:   "20001",
		Text:             "hello",
		CommandPrefix:    "/",
		PrivateChat:      "admins",
		Enabled:          true,
	}
}

func privateInput() DecideInput {
	in := groupInput()
	in.ConversationType = conloomapi.ConvPrivate
	in.Enabled = false
	return in
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*DecideInput)
		base   func() DecideInput
		want   Decision
	}{
		{
			name: "self message ignored",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.PlatformUserID = "30001"
				in.Mentions = []conloomapi.Mention{{Type: "bot", ID: "bot_main"}}
			},
			want: Decision{Disposition: conloomapi.DispositionIgnore, Reason: conloomapi.SuppressedSelfMessage},
		},
		{
			name: "group not enabled ignored",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.Enabled = false
				in.Text = "/help"
			},
			want: Decision{Disposition: conloomapi.DispositionIgnore, Reason: conloomapi.SuppressedNotEnabled},
		},
		{
			name: "private off",
			base: privateInput,
			modify: func(in *DecideInput) {
				in.PrivateChat = "off"
				in.IsAdmin = true
			},
			want: Decision{Disposition: conloomapi.DispositionIgnore, Reason: conloomapi.SuppressedPrivateNotAllowed},
		},
		{
			name: "private admins non-admin",
			base: privateInput,
			modify: func(in *DecideInput) {
				in.PrivateChat = "admins"
				in.Text = "/help"
			},
			want: Decision{Disposition: conloomapi.DispositionIgnore, Reason: conloomapi.SuppressedPrivateNotAllowed},
		},
		{
			name: "private admin replies",
			base: privateInput,
			modify: func(in *DecideInput) {
				in.PrivateChat = "admins"
				in.PlatformUserID = "29999"
				in.IsAdmin = true
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerPrivate},
		},
		{
			name: "private all",
			base: privateInput,
			modify: func(in *DecideInput) {
				in.PrivateChat = "all"
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerPrivate},
		},
		{
			name: "command in group",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.Text = "/persona show"
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerCommand, Command: "persona", Args: "show"},
		},
		{
			name: "unknown slash text observes",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.Text = "/foo bar"
			},
			want: Decision{Disposition: conloomapi.DispositionObserve},
		},
		{
			name: "mention bot",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20002"}, {Type: "bot", ID: "bot_main"}}
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerMention},
		},
		{
			name: "reply to bot",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.ReplyToIsBot = true
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerReply},
		},
		{
			name: "continuation",
			base: groupInput,
			modify: func(in *DecideInput) {
				in.InContinuation = true
			},
			want: Decision{Disposition: conloomapi.DispositionReply, Trigger: "continuation"},
		},
		{
			name:   "plain group message observes",
			base:   groupInput,
			modify: func(in *DecideInput) {},
			want:   Decision{Disposition: conloomapi.DispositionObserve},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.base()
			tt.modify(&in)
			if got := Decide(in); got != tt.want {
				t.Fatalf("Decide() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecide_MentionOfOtherUserObserves(t *testing.T) {
	in := groupInput()
	in.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20002"}}
	if got := Decide(in); got.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("Decide() = %+v, want observe", got)
	}
}

func TestDecide_AtAllObserves(t *testing.T) {
	in := groupInput()
	in.Mentions = []conloomapi.Mention{{Type: "all", ID: "all"}}
	if got := Decide(in); got.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("Decide() = %+v, want observe", got)
	}
}

func TestDecide_MentionOfOtherBotInstanceObserves(t *testing.T) {
	in := groupInput()
	in.Mentions = []conloomapi.Mention{{Type: "bot", ID: "bot_other"}}
	if got := Decide(in); got.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("Decide() = %+v, want observe", got)
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		text, prefix string
		cmd, args    string
		ok           bool
	}{
		{" /Help me", "/", "help", "me", true},
		{"/foo", "/", "", "", false},
		{"hi /help", "/", "", "", false},
		{"/stop", "/", "stop", "", true},
		{"/persona  set  cute ", "/", "persona", "set  cute", true},
		{"/ help", "/", "", "", false},
		{"/helpme", "/", "", "", false},
		{"!pause now", "!", "pause", "now", true},
		{"/pause", "!", "", "", false},
		{"help", "", "", "", false},
	}
	for _, tt := range tests {
		cmd, args, ok := ParseCommand(tt.text, tt.prefix)
		if cmd != tt.cmd || args != tt.args || ok != tt.ok {
			t.Errorf("ParseCommand(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.text, tt.prefix, cmd, args, ok, tt.cmd, tt.args, tt.ok)
		}
	}
}
