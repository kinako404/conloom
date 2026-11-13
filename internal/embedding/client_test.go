package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const fakeKey = "fake-embed-key-123"

type request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// fakeServer answers embeddings requests with vectors derived from each
// input's length, in reverse order of index to check that the client sorts
// by index.
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []request
	auth     []string
	paths    []string
	conns    atomic.Int32
}

func newFakeServer(t *testing.T, handler func(w http.ResponseWriter, req request) bool) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if handler != nil && handler(w, req) {
			return
		}
		writeVectors(w, req.Input)
	}))
	f.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			f.conns.Add(1)
		}
	}
	f.Start()
	t.Cleanup(f.Close)
	return f
}

func writeVectors(w http.ResponseWriter, input []string) {
	type item struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	var data []item
	for i := len(input) - 1; i >= 0; i-- {
		data = append(data, item{Index: i, Embedding: []float64{float64(len([]rune(input[i]))), 1, 0.5}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func newClient(url string) *Client {
	return New(config.EmbeddingConfig{
		Endpoint: url + "/v1/", Model: "bge-m3", APIKey: fakeKey,
		Timeout: conloomapi.Duration{Duration: 2 * time.Second}, BatchSize: 16,
	}, nil)
}

func TestEmbed_SendsBatchAndOrdersByIndex(t *testing.T) {
	f := newFakeServer(t, nil)
	c := newClient(f.URL)

	vecs, err := c.Embed(context.Background(), []string{"小明", "小红你好", "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 || vecs[0][0] != 2 || vecs[1][0] != 4 || vecs[2][0] != 1 || len(vecs[0]) != 3 {
		t.Fatalf("vecs = %v", vecs)
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests = %d, want one batched request", len(f.requests))
	}
	if r := f.requests[0]; r.Model != "bge-m3" || len(r.Input) != 3 || r.Input[1] != "小红你好" {
		t.Errorf("request = %+v", r)
	}
	if f.paths[0] != "POST /v1/embeddings" {
		t.Errorf("path = %q", f.paths[0])
	}
	if f.auth[0] != "Bearer "+fakeKey {
		t.Errorf("Authorization = %q", f.auth[0])
	}
	if c.Model() != "bge-m3" || c.BatchSize() != 16 {
		t.Errorf("Model/BatchSize = %q/%d", c.Model(), c.BatchSize())
	}
}

func TestEmbed_NoKeyNoAuthorization(t *testing.T) {
	f := newFakeServer(t, nil)
	c := New(config.EmbeddingConfig{Endpoint: f.URL, Model: "bge-m3", Timeout: conloomapi.Duration{Duration: time.Second}, BatchSize: 4}, nil)
	if _, err := c.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if f.auth[0] != "" || f.paths[0] != "POST /embeddings" {
		t.Errorf("auth/path = %q/%q", f.auth[0], f.paths[0])
	}
}

func TestEmbed_ReusesConnections(t *testing.T) {
	f := newFakeServer(t, nil)
	c := newClient(f.URL)
	for range 5 {
		if _, err := c.Embed(context.Background(), []string{"小明"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.conns.Load(); n != 1 {
		t.Errorf("opened %d connections for 5 sequential requests, want 1 (keep-alive)", n)
	}
}

func TestEmbed_EmptyInputSkipsRequest(t *testing.T) {
	f := newFakeServer(t, nil)
	vecs, err := newClient(f.URL).Embed(context.Background(), nil)
	if err != nil || vecs != nil || len(f.requests) != 0 {
		t.Fatalf("vecs=%v err=%v requests=%d", vecs, err, len(f.requests))
	}
}

func TestEmbed_StatusErrorHidesKey(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, _ request) bool {
		http.Error(w, "invalid key "+fakeKey, http.StatusUnauthorized)
		return true
	})
	_, err := newClient(f.URL).Embed(context.Background(), []string{"小明"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 StatusError", err)
	}
	if strings.Contains(err.Error(), fakeKey) {
		t.Errorf("error %q quotes the api key", err)
	}
	if Result(err) != ResultError {
		t.Errorf("Result = %q, want error", Result(err))
	}
}

func TestEmbed_RejectsMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"not json":      `oops`,
		"too few":       `{"data":[{"index":0,"embedding":[1,2]}]}`,
		"bad index":     `{"data":[{"index":0,"embedding":[1,2]},{"index":5,"embedding":[1,2]}]}`,
		"dup index":     `{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[1,2]}]}`,
		"empty vector":  `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[]}]}`,
		"mixed lengths": `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1]}]}`,
		"NaN":           `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[NaN,2]}]}`,
		"Inf":           `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1e400,2]}]}`,
		"over float32":  `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1e39,2]}]}`,
		"-over float32": `{"data":[{"index":0,"embedding":[1,-1e39]},{"index":1,"embedding":[1,2]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, _ request) bool {
				_, _ = w.Write([]byte(body))
				return true
			})
			vecs, err := newClient(f.URL).Embed(context.Background(), []string{"a", "b"})
			if err == nil || vecs != nil {
				t.Fatalf("Embed = %v, %v; want an error", vecs, err)
			}
			if Result(err) != ResultError {
				t.Errorf("Result(%v) = %q, want error", err, Result(err))
			}
		})
	}
}

func TestEmbed_TimeoutIsDistinguished(t *testing.T) {
	release := make(chan struct{})
	f := newFakeServer(t, func(w http.ResponseWriter, _ request) bool {
		<-release
		return false
	})
	defer close(release)
	c := New(config.EmbeddingConfig{
		Endpoint: f.URL, Model: "bge-m3", APIKey: fakeKey,
		Timeout: conloomapi.Duration{Duration: 50 * time.Millisecond}, BatchSize: 4,
	}, nil)
	start := time.Now()
	_, err := c.Embed(context.Background(), []string{"小明"})
	if err == nil {
		t.Fatal("Embed succeeded, want timeout")
	}
	if time.Since(start) > time.Second {
		t.Errorf("Embed took %s, want it bounded by the 50ms timeout", time.Since(start))
	}
	if Result(err) != ResultTimeout {
		t.Errorf("Result(%v) = %q, want timeout", err, Result(err))
	}
	if strings.Contains(err.Error(), fakeKey) {
		t.Errorf("error %q quotes the api key", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := newClient(f.URL).Embed(ctx, []string{"小明"}); Result(err) != ResultTimeout {
		t.Errorf("caller deadline: Result(%v) = %q, want timeout", err, Result(err))
	}
	if Result(nil) != ResultOK {
		t.Errorf("Result(nil) = %q", Result(nil))
	}
}
