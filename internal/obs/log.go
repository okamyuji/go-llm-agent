package obs

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// LoggerOptions ロガー設定
type LoggerOptions struct {
	Format string
	Writer io.Writer
	Level  string
}

// NewLogger オプションから slog.Logger を生成して返す
func NewLogger(opt LoggerOptions) *slog.Logger {
	w := opt.Writer
	if w == nil {
		w = os.Stderr
	}
	lvl := parseLevel(opt.Level)
	var h slog.Handler
	switch strings.ToLower(opt.Format) {
	case "json":
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	default:
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
