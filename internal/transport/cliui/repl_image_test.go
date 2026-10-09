package cliui_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
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
	cases := map[string]string{
		"no args":      "/image\n",
		"no question":  "/image " + p + "\n",
		"missing file": "/image " + filepath.Join(t.TempDir(), "x.png") + " q\n",
		"spaced path":  "/image " + filepath.Join(t.TempDir(), "a b.png") + " q\n",
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
