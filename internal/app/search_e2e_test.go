package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const notFoundResult = toolPreface + "没有找到相关的聊天记录。"

// recallQuestions maps each question the tests ask to the recall arguments
// the fake model calls with; each answer is "好的。" and the question.
var recallQuestions = map[string]string{
	"团子是谁来着":       `{"query":"团子"}`,
	"我说过周末去哪玩吗":    `{"query":"出去玩"}`,
	"二月以后提过团子吗":    `{"query":"团子","after":"2025-02-01"}`,
	"二月底以前说过出去玩吗":  `{"query":"出去玩","before":"2025-02-28"}`,
	"三月一号那天说过出去玩吗": `{"query":"出去玩","after":"2025-03-01","before":"2025-03-01"}`,
	"现在还记得团子吗":     `{"query":"团子"}`,
	"现在还记得周末去哪玩吗":  `{"query":"出去玩"}`,
	"我今天吃了什么":      `{"query":"草莓"}`,
}

// recallModel calls recall for each of recallQuestions and answers once it
// has the result; other messages get fakeReply.
func recallModel(t *testing.T) *fakeopenai.Server {
	return fakeopenai.NewWithTools(t, func(req map[string]any) fakeopenai.Reply {
		last := strings.TrimSpace(lastUserMessage(req))
		for q, args := range recallQuestions {
			if !strings.HasSuffix(last, q) {
				continue
			}
			if requestToolResults(req) != "" || req["tools"] == nil {
				return fakeopenai.Reply{Content: "好的。" + q}
			}
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "r1", Name: "recall", Arguments: args}}}
		}
		return fakeopenai.Reply{Content: fakeReply}
	})
}

// fakeEmbeddings is an embedding service giving texts about going out one
// direction, texts about 团子 another and everything else a third; the query
// 出去玩 is 0.6 similar to the going-out direction, 0.52 to texts about 爬山
// and unlike the others. With fail set it answers 500.
type fakeEmbeddings struct {
	srv      *httptest.Server
	fail     atomic.Bool
	failures atomic.Int32
	mu       sync.Mutex
	inputs   []string
}

func newFakeEmbeddings(t *testing.T) *fakeEmbeddings {
	f := &fakeEmbeddings{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.fail.Load() {
			f.failures.Add(1)
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.inputs = append(f.inputs, req.Input...)
		f.mu.Unlock()
		data := make([]map[string]any, len(req.Input))
		for i, text := range req.Input {
			v := []float64{0, 0, 1, 0, 0}
			switch {
			case text == "出去玩":
				v = []float64{0.6, 0, 0, 0.8, 0}
			case strings.Contains(text, "出门逛逛"):
				v = []float64{1, 0, 0, 0, 0}
			case strings.Contains(text, "爬山"):
				v = []float64{0.52 / 0.6, 0, 0, 0, math.Sqrt(1 - (0.52/0.6)*(0.52/0.6))}
			case strings.Contains(text, "团子"):
				v = []float64{0, 1, 0, 0, 0}
			}
			data[i] = map[string]any{"index": i, "embedding": v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEmbeddings) embedded(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.inputs {
		if in == text {
			return true
		}
	}
	return false
}

var adminPrivateKey = store.ConversationKey(botID, "qq", conloomapi.ConvPrivate, "private_29999")

// seedHistory writes, before the app starts, a private chat with the admin
// that began long ago: a message about 团子, a weekend plan, a hike, then
// 6000 more messages. It returns the seq of the last one.
func seedHistory(t *testing.T, cfg *config.Config) int64 {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ts := func(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
	start := time.Date(2025, 1, 2, 20, 0, 0, 0, shanghaiLoc(t))
	if _, err := tx.Exec(`INSERT INTO conversations (bot_instance_id, id, key, type, platform, platform_conversation_id, created_at, updated_at)
		VALUES (?, 'private_29999', ?, 'private', 'qq', '29999', ?, ?)`, botID, adminPrivateKey, ts(start), ts(start)); err != nil {
		t.Fatal(err)
	}
	insert := func(i int, fromBot bool, text string, at time.Time) {
		actor, user, name := "user_29999", "29999", "管理员"
		if fromBot {
			actor, user, name = "bot_main", "30001", "织语"
		}
		if _, err := tx.Exec(`INSERT INTO messages (bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
			platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at)
			VALUES (?, ?, ?, 'qq', '', ?, ?, ?, ?, '', '[]', '[]', ?, ?, ?)`,
			botID, fmt.Sprintf("seed_%d", i), adminPrivateKey, actor, user, name, text, fromBot, ts(at), ts(at)); err != nil {
			t.Fatal(err)
		}
	}
	insert(0, false, "我小时候养过一只叫团子的猫", start)
	insert(1, true, "好可爱", start.Add(time.Minute))
	trip := time.Date(2025, 3, 1, 10, 0, 0, 0, shanghaiLoc(t))
	insert(2, false, "周末打算出门逛逛", trip)
	insert(3, true, "去哪里呀", trip.Add(time.Minute))
	insert(4, false, "公园吧", trip.Add(2*time.Minute))
	insert(5, false, "下周去爬山", time.Date(2025, 3, 15, 10, 0, 0, 0, shanghaiLoc(t)))
	later := time.Date(2025, 4, 1, 0, 0, 0, 0, shanghaiLoc(t))
	for i := range 6000 {
		insert(6+i, i%2 == 1, fmt.Sprintf("闲聊第%d句", i), later.Add(time.Duration(i)*time.Minute))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := raw.QueryRow(`SELECT MAX(seq) FROM messages`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	return last
}

func shanghaiLoc(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// queryUntil polls query on the database until it returns true.
func queryUntil(t *testing.T, cfg *config.Config, what, query string, args ...any) {
	t.Helper()
	raw, err := sql.Open("sqlite", cfg.DBPath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var ok bool
		if err := raw.QueryRow(query, args...).Scan(&ok); err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// searchEnv starts an app on a seeded private chat with the admin, with
// memory on and the memory skill, and semantic search when embed is set. It
// waits for the keyword index and, with embed, for the weekend plan's chunk.
func searchEnv(t *testing.T, embed *fakeEmbeddings, configure ...func(*config.Config)) (*env, *fakeopenai.Server, *config.Config, int64) {
	t.Helper()
	fake := recallModel(t)
	cfg := memoryTestConfig(t, fake.URL)
	private := conloomapi.DefaultGroupPolicy()
	private.Memory.Enabled, private.Memory.Backlog, private.Memory.ConsolidateMin = true, 0, 1000
	private.Reply.UserRateLimitPerMinute = 60
	cfg.DefaultPrivatePolicy = &private
	cfg.Bots[0].Skills = []string{"memory"}
	if embed != nil {
		cfg.Embedding = &config.EmbeddingConfig{Endpoint: embed.srv.URL + "/v1", Model: "bge-m3",
			Timeout: conloomapi.Duration{Duration: 5 * time.Second}, BatchSize: 16, MinScore: 0.48}
		for _, fn := range configure {
			fn(cfg)
		}
		if err := cfg.ValidateServe(); err != nil {
			t.Fatal(err)
		}
	}
	last := seedHistory(t, cfg)
	e := newEnvWith(t, cfg, app.Options{EmbeddingInterval: 100 * time.Millisecond})
	queryUntil(t, cfg, "the keyword index backfill", `SELECT backfilled_seq >= ? FROM message_fts_progress`, last)
	if embed != nil {
		queryUntil(t, cfg, "the weekend plan's chunk", `SELECT COUNT(*) > 0 FROM memory_chunks m JOIN messages s ON s.seq BETWEEN m.start_seq AND m.end_seq
			WHERE m.conversation_key = ? AND s.text IN ('周末打算出门逛逛', '下周去爬山') HAVING COUNT(DISTINCT s.text) = 2`, adminPrivateKey)
	}
	return e, fake, cfg, last
}

// ask sends text in the admin's private chat and returns the reply and the
// recall results the model saw for it.
func (e *env) ask(fake *fakeopenai.Server, text string) (string, []string) {
	e.t.Helper()
	resp, _ := e.say(mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}, admin, text, mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply {
		e.t.Fatalf("private %q: response %+v, want a reply task", text, resp)
	}
	return e.drainOne().Content.Text, toolMessagesOf(fake, text)
}

func TestE2E_RecallSearchesTheWholeHistoryWithVectors(t *testing.T) {
	embed := newFakeEmbeddings(t)
	e, fake, cfg, last := searchEnv(t, embed)
	one := func(q string) string {
		t.Helper()
		reply, results := e.ask(fake, q)
		if reply != "好的。"+q || len(results) != 1 {
			t.Fatalf("%s: reply %q, recall results %q", q, reply, results)
		}
		return results[0]
	}

	if got := one("团子是谁来着"); !strings.Contains(got, "—— 2025-01-02 ——\n[01-02 20:00] 管理员：我小时候养过一只叫团子的猫") {
		t.Errorf("keyword recall of a message 6000 messages back =\n%s", got)
	}
	if got := one("我说过周末去哪玩吗"); !strings.Contains(got, "—— 2025-03-01 ——\n[03-01 10:00] 管理员：周末打算出门逛逛") ||
		strings.Contains(got, "闲聊") || strings.Contains(got, "团子") || strings.Contains(got, "爬山") {
		t.Errorf("semantic recall =\n%s", got)
	}
	if !embed.embedded("出去玩") {
		t.Error("the query was not embedded")
	}
	if got := one("二月以后提过团子吗"); strings.Contains(got, "养过一只叫团子的猫") || !strings.Contains(got, "管理员：团子是谁来着") {
		t.Errorf("recall after 2025-02-01 =\n%s", got)
	}
	if got := one("二月底以前说过出去玩吗"); got != notFoundResult {
		t.Errorf("recall before 2025-02-28 =\n%s", got)
	}
	if got := one("三月一号那天说过出去玩吗"); !strings.HasPrefix(got, toolPreface+"—— 2025-03-01 ——\n[03-01 10:00] 管理员：周末打算出门逛逛") {
		t.Errorf("recall on 2025-03-01 =\n%s", got)
	}

	resp, _ := e.say(mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}, admin, "/忘记我", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("/忘记我: %+v", resp)
	}
	e.drainOne()
	queryUntil(t, cfg, "the old chunks to go", `SELECT COUNT(*) = 0 FROM memory_chunks WHERE conversation_key = ? AND start_seq <= ?`,
		adminPrivateKey, last)
	if got := one("现在还记得团子吗"); got != notFoundResult {
		t.Errorf("keyword recall after /忘记我 =\n%s", got)
	}
	if got := one("现在还记得周末去哪玩吗"); got != notFoundResult {
		t.Errorf("semantic recall after /忘记我 =\n%s", got)
	}

	if reply, _ := e.ask(fake, "我今天吃了草莓"); reply != fakeReply {
		t.Fatalf("reply = %q", reply)
	}
	embed.fail.Store(true)
	if got := one("我今天吃了什么"); !strings.Contains(got, "管理员：我今天吃了草莓") {
		t.Errorf("recall with the embedding service down =\n%s", got)
	}
	if embed.failures.Load() == 0 {
		t.Error("recall did not try the embedding service")
	}
}

func TestE2E_RecallWithoutEmbeddingUsesKeywords(t *testing.T) {
	e, fake, _, _ := searchEnv(t, nil)
	if e.app.VectorSearcher() != nil {
		t.Fatal("VectorSearcher without an embedding section")
	}
	reply, got := e.ask(fake, "团子是谁来着")
	if reply != "好的。团子是谁来着" || len(got) != 1 || !strings.Contains(got[0], "管理员：我小时候养过一只叫团子的猫") {
		t.Fatalf("keyword recall: reply %q, results %q", reply, got)
	}
	if _, got := e.ask(fake, "我说过周末去哪玩吗"); len(got) != 1 || got[0] != notFoundResult {
		t.Fatalf("a reworded question without semantic search = %q", got)
	}
}

func TestE2E_RecallSemanticFloorFromConfig(t *testing.T) {
	e, fake, _, _ := searchEnv(t, newFakeEmbeddings(t), func(cfg *config.Config) { cfg.Embedding.MinScore = 0.7 })
	if _, got := e.ask(fake, "我说过周末去哪玩吗"); len(got) != 1 || got[0] != notFoundResult {
		t.Fatalf("a 0.6 similar stretch under min_score 0.7 = %q", got)
	}
	if _, got := e.ask(fake, "团子是谁来着"); len(got) != 1 || !strings.Contains(got[0], "叫团子的猫") {
		t.Fatalf("keyword recall under min_score 0.7 = %q", got)
	}
}
