package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/router"
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
	base := consPromptTokens(buildPrompt(consStyleFor(testConvKey), "S1", nil, nil), 1)
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

func TestConsMemberCap(t *testing.T) {
	for _, c := range []struct{ out, summary, note, want int }{
		{0, 1500, 300, maxBatchMessages}, {-1, 3000, 1000, maxBatchMessages}, {2000, 1500, 300, 1}, {3400, 1500, 300, 2}, {4096, 1500, 300, 3}, {8192, 1500, 300, 11},
		{4096, 3000, 1000, 1}, {8192, 3000, 1000, 2}, {8192, 8000, 2000, 1}, {4096, 400, 80, 16},
	} {
		if got := consMemberCap(c.out, c.summary, c.note); got != c.want {
			t.Errorf("consMemberCap(%d, %d, %d) = %d, want %d", c.out, c.summary, c.note, got, c.want)
		}
	}
	for _, c := range []struct{ summary, note int }{{1500, 300}, {3000, 1000}} {
		n := consMemberCap(8192, c.summary, c.note)
		if planned := consPlannedOutput(n, c.summary, c.note); float64(planned) > 8192*consOutputShare || planned < c.summary+c.note {
			t.Errorf("%d members with a %d-character summary and %d-character notes plan %d tokens, want within %v of 8192",
				n, c.summary, c.note, planned, consOutputShare)
		}
	}
}

func TestConsPlanBatch_CutsAtMemberCap(t *testing.T) {
	reply := consRec(3, "m3", "20001", "小明", "对", consAt(2))
	reply.ReplyTo = "m2"
	msgs := []store.MessageRecord{
		consRec(1, "m1", "20001", "小明", "早", consAt(0)),
		consRec(2, "m2", "20002", "小红", "早啊", consAt(1)),
		reply,
		consRec(4, "m4", "20003", "", "大家好", consAt(3)),
		consRec(5, "m5", "20001", "小明", "你好", consAt(4)),
	}
	in := consBatchInput{ConversationKey: testConvKey, Messages: msgs, BotName: "织语", Location: time.UTC,
		Budget: 100000, TokenFactor: 1, ReplyTargets: map[string]store.MessageRecord{"m2": msgs[1]}}
	for cap, want := range map[int]struct {
		lines   int
		lastSeq int64
	}{0: {5, 5}, 1: {1, 1}, 2: {3, 3}, 3: {5, 5}} {
		in.MaxMembers = cap
		b := consPlanBatch(in)
		if len(b.Lines) != want.lines || b.LastSeq != want.lastSeq || (cap > 0 && len(b.Members) > cap) {
			t.Errorf("MaxMembers %d: %d lines up to seq %d with %d members; want %d lines up to seq %d",
				cap, len(b.Lines), b.LastSeq, len(b.Members), want.lines, want.lastSeq)
		}
	}
	single := consRec(1, "m1", "20001", "小明", "叫上你们", consAt(0))
	single.Mentions = []conloomapi.Mention{{Type: "user", ID: "user_20002"}, {Type: "user", ID: "user_20003"}}
	in.Messages, in.MaxMembers = []store.MessageRecord{single, msgs[1]}, 1
	in.MentionUsers = map[string]string{"user_20002": "20002", "user_20003": "20003"}
	if b := consPlanBatch(in); len(b.Lines) != 2 || len(b.Members) != 3 {
		t.Errorf("one message over the cap: %d lines, %d members; want it kept, then a message adding nobody", len(b.Lines), len(b.Members))
	}
}

func TestConsolidate_OutputBudgetSplitsManyMembers(t *testing.T) {
	e := newConsEnv(t, consOpts{reservedOutput: 3400})
	e.add(t,
		e.msg("20001", "小明", "早", consAt(0)),
		e.msg("20002", "小红", "早啊", consAt(1)),
		e.msg("20003", "", "大家好", consAt(2)),
		e.msg("20001", "小明", "你好", consAt(3)),
	)
	res := e.run(t, false)
	if res.Batches != 2 || e.long.calls() != 2 {
		t.Fatalf("Result = %+v with %d calls, want 2 batches", res, e.long.calls())
	}
	for i, want := range [][]string{
		{"[09:00] 小明（P1）：早", "[09:01] 小红（P2）：早啊"},
		{"[09:02] 一位群友（P1）：大家好", "[09:03] 小明（P2）：你好"},
	} {
		if got := consBatchLines(t, e.long.request(t, i)); !slices.Equal(got, want) {
			t.Errorf("batch %d lines = %q, want %q", i, got, want)
		}
	}
}

// consRoundRobin stores n messages from 20001, 20002 and 20003 in turn.
func consRoundRobin(t *testing.T, e *consEnv, n int) []store.MessageRecord {
	t.Helper()
	users := []struct{ id, name string }{{"20001", "小明"}, {"20002", "小红"}, {"20003", ""}}
	var msgs []store.MessageRecord
	for i := range n {
		u := users[i%len(users)]
		msgs = append(msgs, e.msg(u.id, u.name, "第"+strconv.Itoa(i)+"条", consAt(i)))
	}
	return e.add(t, msgs...)
}

func TestConsolidate_TruncatedOutputShrinksNextBatch(t *testing.T) {
	e := newConsEnv(t, consOpts{reservedOutput: 4096})
	consRoundRobin(t, e, 30)
	e.long.setReply(func(int, openai.ChatRequest) (openai.ChatResponse, error) {
		return openai.ChatResponse{Content: `{"group_summary":"S","members":[{"ref":"P1","note":"写到一半`, Model: "fake-long",
			FinishReason: "length", CompletionTokens: 4096}, nil
	})
	if _, err := e.c.Run(context.Background(), testConvKey, false); err == nil {
		t.Fatal("Run: want an error for truncated output")
	}
	if got := len(consBatchLines(t, e.long.request(t, 0))); got != 30 {
		t.Fatalf("first batch has %d lines, want all 30", got)
	}
	if g := e.group(t); g.CursorSeq != 0 || g.LastError != consErrTruncated {
		t.Fatalf("group = %+v, want cursor 0 and %s", g, consErrTruncated)
	}
	logs := e.logs.String()
	for _, want := range []string{"finish_reason=length", "completion_tokens=4096"} {
		if !strings.Contains(logs, want) {
			t.Errorf("failure log lacks %q:\n%s", want, logs)
		}
	}

	e.long.answer(`{"group_summary":"S","members":[]}`)
	if res := e.run(t, false); res.Batches != 3 {
		t.Fatalf("Result = %+v, want 3 batches", res)
	}
	var sizes []int
	for i := 1; i < e.long.calls(); i++ {
		sizes = append(sizes, len(consBatchLines(t, e.long.request(t, i))))
	}
	if !slices.Equal(sizes, []int{1, 2, 27}) {
		t.Errorf("batch sizes after truncation = %v, want [1 2 27] (1 member, then 2, then the default 3)", sizes)
	}
}

func TestConsolidate_TimeoutShrinksMessagesToFloor(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	var msgs []store.MessageRecord
	for i := range 60 {
		msgs = append(msgs, e.msg("20001", "小明", "第"+strconv.Itoa(i)+"条", consAt(i)))
	}
	msgs = e.add(t, msgs...)
	e.long.setReply(func(int, openai.ChatRequest) (openai.ChatResponse, error) {
		return openai.ChatResponse{}, fmt.Errorf("openai: %w", context.DeadlineExceeded)
	})
	for i := range 3 {
		if _, err := e.c.Run(context.Background(), testConvKey, true); err == nil {
			t.Fatalf("run %d: want a timeout error", i)
		}
	}
	var sizes []int
	for i := range 3 {
		sizes = append(sizes, len(consBatchLines(t, e.long.request(t, i))))
	}
	if !slices.Equal(sizes, []int{60, 30, 20}) {
		t.Fatalf("timed-out batch sizes = %v, want [60 30 20] (halved, floor 20)", sizes)
	}
	if g := e.group(t); g.CursorSeq != 0 || g.LastError != consErrModel {
		t.Fatalf("group = %+v, want cursor 0 and %s", g, consErrModel)
	}

	e.long.answer(`{"group_summary":"S","members":[]}`)
	if res := e.run(t, true); res.Batches != 2 || res.Messages != 60 {
		t.Fatalf("Result = %+v, want 60 messages in 2 batches", res)
	}
	if a, b := len(consBatchLines(t, e.long.request(t, 3))), len(consBatchLines(t, e.long.request(t, 4))); a != 20 || b != 40 {
		t.Errorf("batch sizes after timeouts = %d, %d; want 20 (floor) then 40 (doubled)", a, b)
	}
	if g := e.group(t); g.CursorSeq != msgs[59].Seq {
		t.Errorf("cursor = %d, want %d", g.CursorSeq, msgs[59].Seq)
	}
}

func TestConsolidate_BusyWhenAdmissionWaitExpires(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	consSeedThree(t, e)
	e.c.admissionWait = 20 * time.Millisecond
	hold, err := e.adm.Acquire(context.Background(), admission.Request{ID: "hold", Profile: string(router.RoleLong), Tokens: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer hold.Release()
	for range 4 {
		if res := e.run(t, false); res.Skipped != "busy" {
			t.Fatalf("Run = %+v, want busy (never backoff)", res)
		}
	}
	if e.long.calls() != 0 {
		t.Errorf("long calls = %d, want 0", e.long.calls())
	}
	if got := e.counter(t, "conloom_memory_consolidations_total", "skipped"); got != 4 {
		t.Errorf("consolidations{skipped} = %v, want 4", got)
	}
	if got := e.counter(t, "conloom_memory_consolidations_total", "error"); got != 0 {
		t.Errorf("consolidations{error} = %v, want 0", got)
	}
	if _, err := e.st.GetGroupMemory(context.Background(), testConvKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetGroupMemory err = %v, want no row", err)
	}
}

func TestConsPlanBatch_NameThatIsTheUserIDShownGenerically(t *testing.T) {
	target := consRec(1, "m1", "20002", "20002", "早", consAt(0))
	reply := consRec(2, "m2", "20003", "20003", "早啊", consAt(1))
	reply.ReplyTo = "m1"
	b := consPlanBatch(consBatchInput{
		ConversationKey: testConvKey,
		Messages:        []store.MessageRecord{reply},
		Members: map[string]store.MemberMemory{
			"20002": {ConversationKey: testConvKey, PlatformUserID: "20002", DisplayName: "20002", Version: 1},
		},
		ReplyTargets: map[string]store.MessageRecord{"m1": target},
		BotName:      "织语",
		Location:     time.UTC,
		Budget:       100000,
		TokenFactor:  1,
	})
	if want := []string{"[09:01] 一位群友（P1）（回复 一位群友（P2） 09:00 的「早」）：早啊"}; !slices.Equal(b.Lines, want) {
		t.Fatalf("Lines = %q, want %q", b.Lines, want)
	}
	var members []string
	for _, m := range b.Members {
		if m.Name != "" {
			t.Errorf("member %s Name = %q, want empty (an ID is not a name)", m.UserID, m.Name)
		}
		members = append(members, consMemberLine(consPromptMember(m.Memory, m.Ref, m.Name, "")))
	}
	want := []string{"P1 一位群友：笔记：暂无；好感度：未建立", "P2 一位群友：笔记：暂无；好感度：未建立"}
	if !slices.Equal(members, want) {
		t.Errorf("member lines = %q, want %q", members, want)
	}
	consAssertNoIDs(t, strings.Join(append(b.Lines, members...), "\n"))
}
