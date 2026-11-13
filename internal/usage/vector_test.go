package usage_test

import (
	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/search/vector"
	"github.com/kinako404/conloom/internal/usage"
)

var (
	_ vector.Embedder   = (*usage.Embedder)(nil)
	_ usage.EmbedClient = (*embedding.Client)(nil)
)
