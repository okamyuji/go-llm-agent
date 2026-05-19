package obs_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/obs"
)

func TestNewLogger_JSON(t *testing.T) {
	var buf bytes.Buffer
	l := obs.NewLogger(obs.LoggerOptions{Format: "json", Writer: &buf, Level: "info"})
	l.Info("hello", "key", "value")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Fatalf("JSON 形式で msg を出すこと got=%q", buf.String())
	}
}

func TestNewLogger_Text(t *testing.T) {
	var buf bytes.Buffer
	l := obs.NewLogger(obs.LoggerOptions{Format: "text", Writer: &buf, Level: "debug"})
	l.Debug("dbg", "k", "v")
	if !strings.Contains(buf.String(), "msg=dbg") {
		t.Fatalf("Text 形式で msg=dbg を出すこと got=%q", buf.String())
	}
}
