package app_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	memAdminToken  = "fake-admin-token"
	memConsolidate = `{"group_summary":"大家在聊猫","members":[{"ref":"P1","note":"喜欢猫","affinity_delta":1}]}`
	memBlockHeader = "【你记得的事"
)

// memoryConfigTemplate is configTemplate with a long_context profile on the
// same fake model and an admin token that can change policies and memory.
var memoryConfigTemplate = strings.NewReplacer(
	"    scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]\n",
	"    scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]\n"+
		"  - name: admin\n"+
		"    token: "+memAdminToken+"\n"+
		"    bot_instance_id: bot_main\n"+
		"    scopes: [policy:write, admin:read, memory:read, memory:write]\n",
	"model_profiles:\n",
	"model_profiles:\n"+
		"  long_context:\n"+
		"    endpoint: MODEL_URL\n"+
		"    model: fake-long-model\n"+
		"    api_key: fake-model-key\n"+
		"    modalities: [text]\n"+
		"    context: {native_window: 131072, configured_window: 131072, soft_input_budget: 64000, reserved_output_tokens: 2048, safety_margin_tokens: 1024}\n"+
		"    runtime: {max_concurrency: 1, timeout: 10s, max_retries: 0}\n"+
		"    privacy: {data_location: remote, allow_sensitive_content: true}\n",
).Replace(configTemplate)

// memoryTestConfig writes memoryConfigTemplate for a fake model at modelURL
// and loads it.
func memoryTestConfig(t *testing.T, modelURL string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	yml := strings.NewReplacer(
		"DATA_DIR", strconv.Quote(filepath.Join(dir, "data")),
		"MODEL_URL", modelURL,
		"MODEL_NAME", "fake-model",
		"LEASE", "60s",
		"DEBOUNCE", "20ms",
		"RATE_LIMIT", "30",
		"CONTINUATION", "0s",
		"COOLDOWN", "30m",
		"TURN_GAP", "6s",
		"TURN_MAX_WAIT", "20s",
		"TURN_COOLDOWN", "45s",
	).Replace(memoryConfigTemplate)
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

// memoryModel answers consolidation requests with memConsolidate and every
// other request with fakeReply.
func memoryModel(t *testing.T) *fakeopenai.Server {
	return fakeopenai.New(t, func(req map[string]any) (int, string) {
		if strings.Contains(requestText(req), memory.LabelBatch) {
			return 200, memConsolidate
		}
		return 200, fakeReply
	})
}

// lastUserMessage is the content of the request's last user message.
func lastUserMessage(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	for _, m := range slices.Backward(msgs) {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "user" {
			c, _ := mm["content"].(string)
			return c
		}
	}
	return ""
}

// replyRequests are the requests that are not consolidations.
func replyRequests(fake *fakeopenai.Server) []map[string]any {
	return slices.DeleteFunc(fake.Requests(), func(req map[string]any) bool {
		return strings.Contains(requestText(req), memory.LabelBatch)
	})
}

// memberOf returns the member userID in st.
func memberOf(st conloomapi.MemoryState, userID string) (conloomapi.MemberMemoryState, bool) {
	for _, m := range st.Members {
		if m.UserID == userID {
			return m, true
		}
	}
	return conloomapi.MemberMemoryState{}, false
}

// awayFromMidnight waits until the local day in the server's timezone has at
// least a minute left, so the daily affinity rules see one day throughout.
func awayFromMidnight(t *testing.T) {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	y, m, d := now.Date()
	if left := time.Date(y, m, d+1, 0, 0, 0, 0, loc).Sub(now); left < time.Minute {
		time.Sleep(left + time.Second)
	}
}

func TestE2E_MemoryEnableConsolidateInjectCommands(t *testing.T) {
	awayFromMidnight(t)
	fake := memoryModel(t)
	e := newEnv(t, memoryTestConfig(t, fake.URL))
	ctx := context.Background()
	adm := client.New(e.srv.URL, memAdminToken, nil)
	const convID = "group_10001"
	getMemory := func() conloomapi.MemoryState {
		t.Helper()
		st, err := adm.GetMemory(ctx, convID)
		if err != nil {
			t.Fatalf("GetMemory: %v", err)
		}
		return st
	}
	setMemory := func(enabled bool) {
		t.Helper()
		mp := conloomapi.DefaultMemoryPolicy()
		mp.Enabled = enabled
		cur, err := adm.GetPolicy(ctx, convID)
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		p := cur.Policy
		p.Memory = mp
		if _, err := adm.PutPolicy(ctx, convID, conloomapi.PolicyPut{Policy: p, Reason: "测试记忆"}); err != nil {
			t.Fatalf("PutPolicy(memory.enabled=%v): %v", enabled, err)
		}
	}

	e.observe(alice, "大家好")
	setMemory(true)

	e.mention(group, alice, "我叫小明，喜欢猫")
	if a := e.drainOne(); a.Content.Text != fakeReply {
		t.Fatalf("reply = %q, want %q", a.Content.Text, fakeReply)
	}
	waitUntil(t, "小明's first affinity", func() bool {
		m, ok := memberOf(getMemory(), alice.PlatformID)
		return ok && m.Affinity != nil && *m.Affinity == 20 && m.Tier == "认识"
	})

	if err := adm.ConsolidateMemory(ctx, convID); err != nil {
		t.Fatalf("ConsolidateMemory: %v", err)
	}
	waitUntil(t, "the consolidated note and summary", func() bool {
		st := getMemory()
		m, _ := memberOf(st, alice.PlatformID)
		return m.Note == "喜欢猫" && st.Summary == "大家在聊猫"
	})
	var consolidations []string
	for _, req := range fake.Requests() {
		if text := requestText(req); strings.Contains(text, memory.LabelBatch) {
			consolidations = append(consolidations, text)
			if req["model"] != "fake-long-model" {
				t.Errorf("consolidation model = %v, want fake-long-model", req["model"])
			}
		}
	}
	if len(consolidations) != 1 {
		t.Fatalf("consolidation requests = %d, want 1", len(consolidations))
	}
	for _, id := range []string{alice.PlatformID, self.PlatformID} {
		if strings.Contains(consolidations[0], id) {
			t.Errorf("consolidation request contains the ID %s:\n%s", id, consolidations[0])
		}
	}
	if !strings.Contains(consolidations[0], "P1 小明") {
		t.Errorf("consolidation request does not list 小明 as P1:\n%s", consolidations[0])
	}

	e.mention(group, alice, "还记得我吗")
	e.drainOne()
	reqs := replyRequests(fake)
	if len(reqs) != 2 {
		t.Fatalf("reply requests = %d, want 2", len(reqs))
	}
	if last := lastUserMessage(reqs[1]); !strings.HasPrefix(last, memBlockHeader) || !strings.Contains(last, "小明（认识）：喜欢猫") {
		t.Errorf("last user message does not start with the memory block about 小明:\n%s", last)
	}

	if got, want := e.commandReply(alice, "/好感度"), "织语对你的好感：认识（21/100）"; got != want {
		t.Errorf("/好感度 reply = %q, want %q", got, want)
	}

	e.commandReply(alice, "/忘记我")
	if _, ok := memberOf(getMemory(), alice.PlatformID); ok {
		t.Errorf("GetMemory still lists 小明 after /忘记我")
	}

	setMemory(false)
	if got, want := e.commandReply(alice, "/记忆"), "这个群没有开启记忆。"; got != want {
		t.Errorf("/记忆 reply with memory off = %q, want %q", got, want)
	}
	e.mention(group, alice, "在吗")
	e.drainOne()
	reqs = replyRequests(fake)
	if len(reqs) != 3 {
		t.Fatalf("reply requests = %d, want 3", len(reqs))
	}
	if text := requestText(reqs[2]); strings.Contains(text, memBlockHeader) {
		t.Errorf("request with memory off has a memory block:\n%s", text)
	}
}

func TestE2E_MemoryDisabledByDefault(t *testing.T) {
	fake := memoryModel(t)
	e := newEnv(t, memoryTestConfig(t, fake.URL))
	ctx := context.Background()

	e.mention(group, alice, "我叫小明，喜欢猫")
	e.drainOne()
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	if text := requestText(reqs[0]); strings.Contains(text, memBlockHeader) {
		t.Errorf("request has a memory block with memory off by default:\n%s", text)
	}
	st, err := client.New(e.srv.URL, memAdminToken, nil).GetMemory(ctx, "group_10001")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if st.Enabled || len(st.Members) != 0 {
		t.Errorf("GetMemory = %+v, want enabled:false and no members", st)
	}
}

func TestMemoryBlockEchoIsStripped(t *testing.T) {
	block := memory.RenderBlock("大家在聊猫", []memory.BlockEntry{
		{Name: "小明", Tier: "认识", Note: "喜欢猫"},
		{Name: "小红", Tier: "熟悉", Note: "养了一只橘猫"},
	})
	if got := contextbuilder.StripTranscriptPrefix(block+"\n\n好的呀", "织语"); got != "好的呀" {
		t.Errorf("echoed memory block stripped to %q, want %q", got, "好的呀")
	}
}

func TestE2E_MemoryNeverForPrivateChats(t *testing.T) {
	fake := memoryModel(t)
	cfg := memoryTestConfig(t, fake.URL)
	cfg.DefaultGroupPolicy.Memory.Enabled = true
	e := newEnv(t, cfg)
	ctx := context.Background()
	adminChat := mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}

	resp, _ := e.say(adminChat, admin, "我叫管理员，喜欢猫", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("private message: response %+v, want a reply task", resp)
	}
	e.drainOne()
	resp, _ = e.say(adminChat, admin, "/记忆 整理", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("/记忆 整理 in a private chat: response %+v, want a command reply", resp)
	}
	if got := e.drainOne().Content.Text; got != core.CopyMemoryGroupOnly {
		t.Errorf("/记忆 整理 in a private chat = %q, want %q", got, core.CopyMemoryGroupOnly)
	}
	if err := client.New(e.srv.URL, memAdminToken, nil).ConsolidateMemory(ctx, "private_29999"); err == nil {
		t.Error("ConsolidateMemory of a private chat succeeded")
	}
	time.Sleep(300 * time.Millisecond)
	for _, req := range fake.Requests() {
		if text := requestText(req); strings.Contains(text, memory.LabelBatch) {
			t.Fatalf("a private chat was consolidated:\n%s", text)
		}
		if last := lastUserMessage(req); strings.HasPrefix(last, memBlockHeader) {
			t.Errorf("a private chat got a memory block:\n%s", last)
		}
	}
}
