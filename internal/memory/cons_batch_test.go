package memory

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func consRec(seq int64, id, user, name, text string, at time.Time) store.MessageRecord {
	return store.MessageRecord{Seq: seq, ID: id, ConversationKey: testConvKey, ActorID: "user_" + user,
		PlatformUserID: user, DisplayName: name, Text: text, OccurredAt: at}
}

func TestConsPlanBatch_HidesForgottenAndAssignsRefs(t *testing.T) {
	forgotten := consAt(10)
	oldTarget := consRec(1, "old", "20001", "小明", "被引用的旧话乙", consAt(0))
	plainTarget := consRec(2, "t2", "20003", "", "普通的话", consAt(4))
	s2 := consRec(12, "m2", "20002", "小红", "你好呀", consAt(1))
	s2.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20001", Name: "小明"}}
	s3 := consRec(13, "m3", "20002", "小红", "哈哈", consAt(11))
	s3.ReplyTo = "old"
	s5 := consRec(15, "m5", "20002", "小红", "同意", consAt(13))
	s5.ReplyTo = "t2"
	s6 := consRec(16, "m6", "20002", "小红", "再见", consAt(14))
	s6.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20001", Name: "小明"}}
	bot := consRec(17, "m7", "30001", "织语", "拜拜", consAt(15))
	bot.ActorID, bot.FromBot = "bot_30001", true
	msgs := []store.MessageRecord{
		consRec(11, "m1", "20001", "小明", "旧正文甲", consAt(0)),
		s2, s3,
		consRec(14, "m4", "20001", "小明", "新正文丙", consAt(12)),
		s5, s6, bot,
	}
	members := map[string]store.MemberMemory{
		"20001": {ConversationKey: testConvKey, PlatformUserID: "20001", ForgottenAt: forgotten, Version: 2},
		"20002": {ConversationKey: testConvKey, PlatformUserID: "20002", Note: "养了狗", Version: 1},
	}
	b := consPlanBatch(consBatchInput{
		ConversationKey: testConvKey,
		Messages:        msgs,
		Members:         members,
		ReplyTargets:    map[string]store.MessageRecord{"old": oldTarget, "t2": plainTarget},
		MentionUsers:    map[string]string{"user_20001": "20001"},
		MentionNames:    map[string]string{"user_20001": "小明"},
		BotName:         "织语",
		Location:        time.UTC,
		Budget:          100000,
		TokenFactor:     1,
	})
	want := []string{
		"[09:01] 小红（P1）（@了 一位群友）：你好呀",
		"[09:11] 小红（P1）（回复了一条更早的消息）：哈哈",
		"[09:12] 小明（P2）：新正文丙",
		"[09:13] 小红（P1）（回复 一位群友（P3） 09:04 的「普通的话」）：同意",
		"[09:14] 小红（P1）（@了 小明（P2））：再见",
		"[09:15] 织语（机器人）：拜拜",
	}
	if !slices.Equal(b.Lines, want) {
		t.Fatalf("Lines =\n%q\nwant\n%q", b.Lines, want)
	}
	joined := strings.Join(b.Lines, "\n")
	consAssertNoIDs(t, joined)
	for _, banned := range []string{"旧正文甲", "被引用的旧话乙"} {
		if strings.Contains(joined, banned) {
			t.Errorf("lines contain forgotten text %q", banned)
		}
	}
	if b.LastSeq != 17 || b.Messages != 7 || b.MemberMessages != 5 {
		t.Errorf("LastSeq %d, Messages %d, MemberMessages %d; want 17, 7, 5", b.LastSeq, b.Messages, b.MemberMessages)
	}
	wantMembers := []consBatchMember{
		{Ref: "P1", UserID: "20002", Name: "小红", Memory: members["20002"]},
		{Ref: "P2", UserID: "20001", Name: "小明", Memory: members["20001"]},
		{Ref: "P3", UserID: "20003", Memory: store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20003"}},
	}
	if len(b.Members) != len(wantMembers) {
		t.Fatalf("Members = %+v, want %+v", b.Members, wantMembers)
	}
	for i, m := range b.Members {
		w := wantMembers[i]
		if m.Ref != w.Ref || m.UserID != w.UserID || m.Name != w.Name || m.Memory.Version != w.Memory.Version ||
			m.Memory.Note != w.Memory.Note || !m.Memory.ForgottenAt.Equal(w.Memory.ForgottenAt) ||
			m.Memory.ConversationKey != testConvKey || m.Memory.PlatformUserID != w.UserID {
			t.Errorf("member %d = %+v, want %+v", i, m, w)
		}
	}
}

func TestConsPlanBatch_CutsAtBudget(t *testing.T) {
	forgotten := consAt(30)
	msgs := []store.MessageRecord{
		consRec(1, "m1", "20001", "小明", strings.Repeat("甲", 50), consAt(0)),
		consRec(2, "m2", "20003", "", "已被忘记的话", consAt(1)),
		consRec(3, "m3", "20002", "小红", strings.Repeat("乙", 50), consAt(2)),
		consRec(4, "m4", "20001", "小明", strings.Repeat("丙", 50), consAt(3)),
	}
	in := consBatchInput{
		ConversationKey: testConvKey,
		Members: map[string]store.MemberMemory{
			"20003": {ConversationKey: testConvKey, PlatformUserID: "20003", ForgottenAt: forgotten, Version: 1},
		},
		BotName:     "织语",
		Location:    time.UTC,
		BaseTokens:  500,
		TokenFactor: 1,
	}
	plan := func(msgs []store.MessageRecord, budget int) consBatch {
		in := in
		in.Messages, in.Budget = msgs, budget
		return consPlanBatch(in)
	}

	three := plan(msgs[:3], 100000)
	if len(three.Lines) != 2 || three.Tokens <= in.BaseTokens {
		t.Fatalf("plan of 3 = %+v", three)
	}
	b := plan(msgs, three.Tokens)
	if len(b.Lines) != 2 || b.LastSeq != 3 || b.Messages != 3 || b.Tokens != three.Tokens || len(b.Members) != 2 {
		t.Fatalf("budget for 3 messages: %+v; want 2 lines up to seq 3", b)
	}
	b = plan(msgs, three.Tokens-1)
	if len(b.Lines) != 1 || b.LastSeq != 2 || b.Messages != 2 || len(b.Members) != 1 || b.Members[0].UserID != "20001" {
		t.Fatalf("budget one short: %+v; want 1 line passing the filtered seq 2", b)
	}
	b = plan(msgs, 1)
	if len(b.Lines) != 1 || b.LastSeq != 2 || b.MemberMessages != 1 {
		t.Fatalf("budget below base: %+v; want the first message kept anyway", b)
	}
	all := plan(msgs, 100000)
	if len(all.Lines) != 3 || all.LastSeq != 4 || all.Messages != 4 {
		t.Fatalf("large budget: %+v; want every message", all)
	}
	onlyForgotten := plan(msgs[1:2], 100000)
	if len(onlyForgotten.Lines) != 0 || onlyForgotten.LastSeq != 2 || onlyForgotten.MemberMessages != 0 {
		t.Fatalf("only a forgotten message: %+v; want it passed with no lines", onlyForgotten)
	}
}

func TestConsolidate_ForgottenMessagesAndQuotesHidden(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	old := e.add(t,
		e.msg("20001", "小明", "被引用的旧话乙", consAt(0)),
		e.msg("20002", "小红", "小红的旧话", consAt(0)),
	)
	if err := e.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: testConvKey, CursorSeq: old[1].Seq}); err != nil {
		t.Fatal(err)
	}
	mention := e.msg("20002", "小红", "你好呀", consAt(2))
	mention.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20001", Name: "小明"}}
	e.add(t, e.msg("20001", "小明", "旧正文甲", consAt(1)), mention)
	if err := e.st.ForgetMember(context.Background(), testConvKey, "20001", consAt(5)); err != nil {
		t.Fatal(err)
	}
	quoteForgotten := e.msg("20002", "小红", "哈哈", consAt(6))
	quoteForgotten.ReplyTo = old[0].ID
	quoteKept := e.msg("20001", "小明", "是的", consAt(8))
	quoteKept.ReplyTo = old[1].ID
	msgs := e.add(t, quoteForgotten, e.msg("20001", "小明", "新正文丙", consAt(7)), quoteKept)
	e.long.answer(`{"group_summary":"","members":[{"ref":"P2","note":"N","affinity_delta":3}]}`)

	e.run(t, false)
	req := e.long.request(t, 0)
	text := consText(req)
	consAssertNoIDs(t, text)
	if strings.Contains(text, "旧正文甲") {
		t.Errorf("request shows a forgotten message body:\n%s", text)
	}
	if strings.Contains(text, "被引用的旧话乙") {
		t.Errorf("request quotes a forgotten message:\n%s", text)
	}
	want := []string{
		"[09:02] 小红（P1）（@了 一位群友）：你好呀",
		"[09:06] 小红（P1）（回复了一条更早的消息）：哈哈",
		"[09:07] 小明（P2）：新正文丙",
		"[09:08] 小明（P2）（回复 小红（P1） 09:00 的「小红的旧话」）：是的",
	}
	if got := consBatchLines(t, req); !slices.Equal(got, want) {
		t.Errorf("batch lines =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(text, "P2 小明：笔记：暂无；好感度：未建立") {
		t.Errorf("request lacks the forgotten member's fresh entry:\n%s", text)
	}
	m := getMember(t, e.st, testConvKey, "20001")
	if m.Note != "N" || m.Affinity != nil || !m.ForgottenAt.Equal(consAt(5)) || m.DisplayName != "小明" {
		t.Errorf("20001 = %+v, want new note N, no affinity, forgotten_at kept", m)
	}
	if g := e.group(t); g.CursorSeq != msgs[2].Seq {
		t.Errorf("cursor = %d, want %d", g.CursorSeq, msgs[2].Seq)
	}
}

func TestConsolidate_SplitsBatchesByBudget(t *testing.T) {
	text := strings.Repeat("猫", 100)
	lineCost := contextbuilder.Estimate("[09:00] 小明（P1）："+text+"\n", 1)
	memberCost := contextbuilder.Estimate(consMemberLine(promptMember{Ref: "P1", Name: "小明"})+"\n", 1)
	base := consPromptTokens(buildPrompt("S1", nil, nil), 1)
	e := newConsEnv(t, consOpts{softBudget: base + 2*memberCost + 25*lineCost})

	var msgs []store.MessageRecord
	for i := range 60 {
		if i%2 == 0 {
			msgs = append(msgs, e.msg("20001", "小明", text, consAt(i)))
		} else {
			msgs = append(msgs, e.msg("20002", "小红", text, consAt(i)))
		}
	}
	msgs = e.add(t, msgs...)

	var sizes []int
	e.long.setReply(func(n int, req openai.ChatRequest) (openai.ChatResponse, error) {
		lines := consBatchLines(t, req)
		consAssertNoIDs(t, consText(req))
		if len(lines) < 1 || len(lines) > 25 {
			t.Errorf("call %d has %d lines, want 1..25", n, len(lines))
		}
		switch n {
		case 2:
			if g := e.group(t); g.CursorSeq != msgs[sizes[0]-1].Seq || g.Summary != "S1" {
				t.Errorf("before call 2 group = %+v, want first batch committed up to seq %d", g, msgs[sizes[0]-1].Seq)
			}
			if !strings.Contains(req.Messages[1].Content, LabelSummary+"\nS1") {
				t.Errorf("call 2 lacks the first summary:\n%s", req.Messages[1].Content)
			}
		case 3:
			return openai.ChatResponse{}, &openai.StatusError{Code: 500, Body: "boom"}
		}
		sizes = append(sizes, len(lines))
		return openai.ChatResponse{Content: `{"group_summary":"S` + string(rune('0'+n)) + `","members":[]}`, Model: "fake-long"}, nil
	})

	if _, err := e.c.Run(context.Background(), testConvKey, false); err == nil {
		t.Fatal("Run: want the third batch's error")
	}
	if len(sizes) != 2 {
		t.Fatalf("successful batches = %v, want 2 before the failure", sizes)
	}
	if g := e.group(t); g.CursorSeq != msgs[sizes[0]+sizes[1]-1].Seq || g.Summary != "S2" || g.LastError == "" {
		t.Fatalf("after failure group = %+v, want cursor at the end of batch 2 and summary S2", g)
	}

	res := e.run(t, false)
	total := 0
	for _, n := range sizes {
		total += n
	}
	if total != 60 || len(sizes) < 3 || e.long.calls() != len(sizes)+1 {
		t.Fatalf("batch sizes %v (calls %d), want 60 lines in at least 3 batches", sizes, e.long.calls())
	}
	if res.Batches != len(sizes)-2 || res.Messages != 60-sizes[0]-sizes[1] {
		t.Errorf("second Result = %+v, sizes %v", res, sizes)
	}
	if g := e.group(t); g.CursorSeq != msgs[59].Seq || g.LastError != "" {
		t.Fatalf("final group = %+v, want cursor %d", g, msgs[59].Seq)
	}
}
