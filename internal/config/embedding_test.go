package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func loadWith(t *testing.T, extra string) *Config {
	t.Helper()
	base, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return mustLoad(t, writeConfig(t, string(base)+extra))
}

func TestLoad_EmbeddingOmitted(t *testing.T) {
	cfg := loadWith(t, "")
	if cfg.Embedding != nil {
		t.Fatalf("Embedding = %+v, want nil without the section", cfg.Embedding)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
}

func TestLoad_EmbeddingDefaults(t *testing.T) {
	cfg := loadWith(t, "embedding:\n  endpoint: http://127.0.0.1:11434/v1\n")
	e := cfg.Embedding
	if e == nil {
		t.Fatal("Embedding = nil, want the section")
	}
	if e.Endpoint != "http://127.0.0.1:11434/v1" || e.Model != "bge-m3" || e.APIKey != "" ||
		e.Timeout.Duration != 10*time.Second || e.BatchSize != 16 {
		t.Errorf("Embedding = %+v", *e)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
}

func TestLoad_EmbeddingExplicit(t *testing.T) {
	cfg := loadWith(t, `embedding:
  endpoint: https://embed.example.invalid/v1
  model: other-embed
  api_key: fake-embed-key
  timeout: 3s
  batch_size: 4
`)
	e := cfg.Embedding
	if e.Endpoint != "https://embed.example.invalid/v1" || e.Model != "other-embed" || e.APIKey != "fake-embed-key" ||
		e.Timeout.Duration != 3*time.Second || e.BatchSize != 4 {
		t.Errorf("Embedding = %+v", *e)
	}
	if !slices.Contains(cfg.Secrets(), "fake-embed-key") {
		t.Errorf("Secrets() = %v, want the embedding api_key", cfg.Secrets())
	}
}

func TestLoad_EmbeddingUnknownFieldRejected(t *testing.T) {
	base, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(writeConfig(t, string(base)+"embedding:\n  endpoint: http://127.0.0.1:11434/v1\n  dims: 1024\n")); err == nil {
		t.Fatal("Load succeeded, want an unknown field error")
	}
}

func TestValidateServe_Embedding(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *EmbeddingConfig)
		want   []string
	}{
		{"empty endpoint", func(e *EmbeddingConfig) { e.Endpoint = "" }, []string{"embedding.endpoint"}},
		{"relative endpoint", func(e *EmbeddingConfig) { e.Endpoint = "127.0.0.1:11434/v1" }, []string{"embedding.endpoint"}},
		{"bad scheme", func(e *EmbeddingConfig) { e.Endpoint = "ftp://127.0.0.1/v1" }, []string{"embedding.endpoint", "http|https"}},
		{"empty model", func(e *EmbeddingConfig) { e.Model = " " }, []string{"embedding.model"}},
		{"zero timeout", func(e *EmbeddingConfig) { e.Timeout.Duration = 0 }, []string{"embedding.timeout"}},
		{"long timeout", func(e *EmbeddingConfig) { e.Timeout.Duration = 6 * time.Minute }, []string{"embedding.timeout"}},
		{"zero batch", func(e *EmbeddingConfig) { e.BatchSize = 0 }, []string{"embedding.batch_size"}},
		{"huge batch", func(e *EmbeddingConfig) { e.BatchSize = 257 }, []string{"embedding.batch_size"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadWith(t, "embedding:\n  endpoint: http://127.0.0.1:11434/v1\n  api_key: fake-embed-key\n")
			tc.mutate(cfg.Embedding)
			err := cfg.ValidateServe()
			if err == nil {
				t.Fatal("ValidateServe succeeded, want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "fake-embed-key") {
				t.Errorf("error %q quotes the api_key", err)
			}
		})
	}

	cfg := loadWith(t, "embedding:\n  endpoint: http://127.0.0.1:11434/v1\n  timeout: 5m\n  batch_size: 256\n")
	if err := cfg.ValidateServe(); err != nil {
		t.Errorf("boundary values: %v", err)
	}
	cfg = loadWith(t, "embedding:\n  endpoint: http://127.0.0.1:11434/v1\n  batch_size: 1\n")
	if err := cfg.ValidateServe(); err != nil {
		t.Errorf("batch_size 1: %v", err)
	}
}
