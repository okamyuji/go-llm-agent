package obs_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/obs"
)

type errCloser struct{ err error }

func (e errCloser) Close() error { return e.err }

func TestCloseLogged_NoError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	obs.CloseLogged(errCloser{nil}, logger, "test")
	if buf.Len() != 0 {
		t.Fatalf("成功時にログ出力したくない got=%q", buf.String())
	}
}

func TestCloseLogged_WithError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	obs.CloseLogged(errCloser{errors.New("boom")}, logger, "stream")
	if !bytes.Contains(buf.Bytes(), []byte("close failed")) {
		t.Fatalf("失敗時に close failed を出すこと got=%q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("stream")) {
		t.Fatalf("where ラベルを出すこと got=%q", buf.String())
	}
}
