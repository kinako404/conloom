package obs

import (
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
)

// NewLogger builds a slog logger from cfg. LogFormat "text" selects the text
// handler; anything else is JSON. LogLevel accepts debug|info|warn|error and
// falls back to info.
func NewLogger(cfg config.ObservabilityConfig, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// MessageAttr returns text=<content> when LogMessageContent is enabled and
// text_len=<rune count> otherwise, so message bodies stay out of logs by default.
func MessageAttr(cfg config.ObservabilityConfig, text string) slog.Attr {
	if cfg.LogMessageContent {
		return slog.String("text", text)
	}
	return slog.Int("text_len", utf8.RuneCountInString(text))
}
