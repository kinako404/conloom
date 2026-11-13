package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestNew_VectorSearcherOnlyWithEmbedding(t *testing.T) {
	model := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: model.URL}))
	if v := e.app.VectorSearcher(); v != nil {
		t.Fatalf("VectorSearcher without an embedding section = %v, want nil", v)
	}

	var mu sync.Mutex
	var got []string
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, r.URL.Path+" "+req.Model+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1, 0}}}})
	}))
	t.Cleanup(embed.Close)
	cfg := testConfig(t, configParams{modelURL: model.URL})
	cfg.Embedding = &config.EmbeddingConfig{Endpoint: embed.URL + "/v1", Model: "bge-m3", APIKey: "fake-embed-key",
		Timeout: conloomapi.Duration{Duration: 5 * time.Second}, BatchSize: 16}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	e = newEnv(t, cfg)
	v := e.app.VectorSearcher()
	if v == nil {
		t.Fatal("VectorSearcher with an embedding section = nil")
	}
	hits, err := v.Search(context.Background(), "bot_main:qq:group:10001", "草莓", search.Range{}, 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("Search with no chunks = %v, %v", hits, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "/v1/embeddings bge-m3 Bearer fake-embed-key" {
		t.Errorf("embedding requests = %q", got)
	}
}
