package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTouchProbe_ExecuteCreatesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "probe.txt")
	res, err := touchProbe{path: p}.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.IsError || res.Content != probeBody {
		t.Fatalf("res=%+v", res)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("副作用ファイルが無い: %v", err)
	}
}

func TestTouchProbe_ExecuteUnwritablePathReturnsToolError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing-dir", "probe.txt")
	res, err := touchProbe{path: p}.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !res.IsError {
		t.Fatalf("書き込み不能でも IsError=false: %+v", res)
	}
}

func TestTouchProbe_SpecName(t *testing.T) {
	if got := (touchProbe{}).Spec().Name; got != toolName {
		t.Fatalf("got=%q", got)
	}
}

func TestVerifyPostPayload_Valid(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"tool":   toolName,
		"args":   json.RawMessage(`{}`),
		"result": map[string]any{"is_error": false, "content": probeBody, "duration_ms": 1},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if err := verifyPostPayload(body); err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyPostPayload_Invalid(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"壊れた JSON", `{`},
		{"ツール名違い", `{"tool":"other","result":{"is_error":false,"content":"` + probeBody + `"}}`},
		{"result 欠落", `{"tool":"` + toolName + `"}`},
		{"is_error=true", `{"tool":"` + toolName + `","result":{"is_error":true,"content":"` + probeBody + `"}}`},
		{"content 不一致", `{"tool":"` + toolName + `","result":{"is_error":false,"content":"other"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := verifyPostPayload([]byte(tt.body)); err == nil {
				t.Fatal("エラーを返すこと")
			}
		})
	}
}

func TestMkdir_CreatesNestedDir(t *testing.T) {
	base := t.TempDir()
	dir, err := mkdir(base, "a")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir=%q err=%v", dir, err)
	}
}

func TestRun_AllKeysEmitted(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), t.TempDir(), &out); err != nil {
		t.Fatalf("run err=%v", err)
	}
	for _, key := range []string{
		"hook_pre_deny_blocks=true",
		"hook_pre_allow_passes=true",
		"hook_post_receives_result=true",
		"hook_pre_timeout_allows=true",
		"hook_parent_cancel_blocks=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in %q", key, out.String())
		}
	}
}
