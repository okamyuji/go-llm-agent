package obs

import (
	"io"
	"log/slog"
)

// CloseLogged Closer を閉じ失敗時に slog でログする
func CloseLogged(c io.Closer, logger *slog.Logger, where string) {
	if err := c.Close(); err != nil {
		logger.Error("close failed", "where", where, "err", err)
	}
}
