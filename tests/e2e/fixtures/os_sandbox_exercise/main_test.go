package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// osSandboxSupportedOnThisPlatform 08-os-sandbox.md の os_sandbox: auto は
// darwin でのみ実際に有効化される (2節)。テストの期待値分岐に使う
func osSandboxSupportedOnThisPlatform() bool {
	return runtime.GOOS == "darwin"
}

func TestRun_MissingAllowFlag(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	code := run("", "/tmp/denied", w, w)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRun_MissingDeniedFlag(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	code := run("/tmp/allow", "", w, w)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestCheckAllowedWrite_Succeeds(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sh := newExerciserShell(logger, dir)
	ok, detail := checkAllowedWrite(context.Background(), sh, dir)
	if !ok {
		t.Fatalf("want ok, detail=%s", detail)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.txt")); err != nil {
		t.Fatalf("ok.txt が作られていること: %v", err)
	}
}

// TestCheckDeniedWrite_OutsideAllowedDirBehavior 非 darwin では os_sandbox が
// no-op のため書込みは成功 (blocked=false)、darwin では sandbox-exec が拒否する
// (blocked=true) ことを、プラットフォームに応じて検証する。t.TempDir() は
// $TMPDIR 配下で無条件許可されるため denied には使えない (08-os-sandbox.md 2節)
func TestCheckDeniedWrite_OutsideAllowedDirBehavior(t *testing.T) {
	allowDir := t.TempDir()
	deniedDir, err := os.MkdirTemp(".", "denied-")
	if err != nil {
		t.Fatalf("MkdirTemp err=%v", err)
	}
	defer os.RemoveAll(deniedDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sh := newExerciserShell(logger, allowDir)
	blocked, detail := checkDeniedWrite(context.Background(), sh, deniedDir)
	wantBlocked := osSandboxSupportedOnThisPlatform()
	if blocked != wantBlocked {
		t.Fatalf("blocked=%v want=%v detail=%s", blocked, wantBlocked, detail)
	}
}

func TestRun_FullFlow(t *testing.T) {
	allowDir := t.TempDir()
	deniedDir, err := os.MkdirTemp(".", "denied-")
	if err != nil {
		t.Fatalf("MkdirTemp err=%v", err)
	}
	defer os.RemoveAll(deniedDir)

	r, w, _ := os.Pipe()
	defer r.Close()
	code := run(allowDir, deniedDir, w, w)
	w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if !osSandboxSupportedOnThisPlatform() {
		// 非 darwin では denied への書込みが no-op sandbox で成功してしまうため
		// blocked=false になり、run は非 0 を返す。それ自体がフィクスチャの
		// 期待どおりの (プラットフォーム由来の) 挙動であることのみ確認する
		if code == 0 {
			t.Fatalf("非 darwin では denied write が成功してしまい run は失敗するはず: out=%s", out)
		}
		return
	}
	if code != 0 {
		t.Fatalf("darwin では run は成功するはず: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "write_to_allowed_ok=true") || !strings.Contains(out, "write_to_denied_blocked=true") {
		t.Fatalf("out=%s", out)
	}
}
