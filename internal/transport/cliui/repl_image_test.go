package cliui_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

func writePNG(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestREPL_ImageSendsImageWithQuestion(t *testing.T) {
	svc := &inputCapturingSvc{}
	p := writePNG(t)
	runSlashREPL(t, svc, cliui.Options{}, "/image "+p+" 何色ですか\n/quit\n")
	if len(svc.inputs) != 1 {
		t.Fatalf("turns = %d, want 1", len(svc.inputs))
	}
	msgs := svc.inputs[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || last.Content != "何色ですか" || len(last.Images) != 1 || last.Images[0].MIMEType != "image/png" {
		t.Errorf("last message = %+v", last)
	}
}

func TestREPL_ImageRejectsBadInputWithoutSending(t *testing.T) {
	p := writePNG(t)
	// 実在する空白入りファイルでも、先頭トークン (存在しない "a") をパスとして扱い何も送らない
	spaced := filepath.Join(t.TempDir(), "a b.png")
	if err := os.WriteFile(spaced, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"no args":      "/image\n",
		"no question":  "/image " + p + "\n",
		"missing file": "/image " + filepath.Join(t.TempDir(), "x.png") + " q\n",
		"spaced path":  "/image " + spaced + " q\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &inputCapturingSvc{}
			got := runSlashREPL(t, svc, cliui.Options{}, in+"/quit\n")
			if len(svc.inputs) != 0 {
				t.Errorf("sent %d turns, want 0", len(svc.inputs))
			}
			if !strings.Contains(got, "[image]") {
				t.Errorf("output lacks [image] message: %q", got)
			}
		})
	}
}

func TestREPL_ImageRecordsPathInSessionJSONL(t *testing.T) {
	dir := t.TempDir()
	p := writePNG(t)
	runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{SessionsDir: dir}, "/image "+p+" 何色ですか\n/quit\n")
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session files = %v, err = %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("[画像: "+p+"]\\n何色ですか")) {
		t.Errorf("JSONL lacks the image marker: %s", b)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(base64.StdEncoding.EncodeToString(raw))) {
		t.Errorf("JSONL contains image bytes: %s", b)
	}
}

// TestREPL_ImageWithUnsupportedProviderShowsError 非対応 provider は HTTP を呼ばずに拒否し、REPL はそのエラーを表示する
func TestREPL_ImageWithUnsupportedProviderShowsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("anthropic must not be called with images")
	}))
	defer srv.Close()
	reg := llm.NewRegistry(map[string]llm.Provider{"anthropic": anthropic.New(anthropic.Options{BaseURL: srv.URL, APIKey: "KEY"})})
	var buf bytes.Buffer
	opt := cliui.Options{
		Model:          "anthropic/m",
		In:             strings.NewReader("/image " + writePNG(t) + " q\n/quit\n"),
		Out:            &buf,
		DisableSpinner: true,
		Registry:       reg,
	}
	if err := cliui.NewREPL(agent.New(reg, tool.NewRegistry(nil, nil)), opt).Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "images not supported by anthropic") {
		t.Errorf("output lacks the unsupported error: %q", buf.String())
	}
}

// TestREPL_ImageLoadErrorsShowPrefixAndPathOnce LoadImage の失敗はどの分岐でも [image] とパスを 1 回ずつだけ表示する
func TestREPL_ImageLoadErrorsShowPrefixAndPathOnce(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	big := write("big.png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	if err := os.Truncate(big, llm.MaxImageBytes+1); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing":   filepath.Join(dir, "x.png"),
		"empty":     write("e.png", nil),
		"not image": write("t.png", []byte("hello world")),
		"too large": big,
		"directory": t.TempDir(),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{}, "/image "+p+" q\n/quit\n")
			if strings.Count(got, "[image]") != 1 || strings.Contains(got, "[image] image:") {
				t.Errorf("want a single [image] prefix: %q", got)
			}
			if n := strings.Count(got, p); n != 1 {
				t.Errorf("path appears %d times, want 1: %q", n, got)
			}
		})
	}
}
