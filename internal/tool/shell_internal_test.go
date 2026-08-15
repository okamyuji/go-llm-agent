package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

func TestOSSandboxEnabled(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		want    bool
	}{
		{"off is always false", "off", false},
		{"auto reflects platform support", "auto", osSandboxPlatformSupported()},
		{"empty (Load を経ない構築) is false", "", false},
		{"unknown value is false", "unexpected", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := osSandboxEnabled(tc.setting); got != tc.want {
				t.Fatalf("osSandboxEnabled(%q) = %v, want %v", tc.setting, got, tc.want)
			}
		})
	}
}

// TestShell_OSSandboxWrapFailure wrapFn がエラーを返す (sandbox-exec 不在等を模擬) とき、
// Execute は IsError:true を返しコマンドを実行しない (フェイルオープン禁止)
func TestShell_OSSandboxWrapFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sh := NewShell(config.ShellToolConfig{
		TimeoutSeconds: 5, MaxTimeoutSeconds: 5,
		AllowBinaries: []string{"echo"},
		OSSandbox:     "auto",
	}, logger, nil)
	// osSandboxEnabled("auto") が false な (非darwin) 環境でもこの経路を
	// 確実に踏ませるため、wrapFn を直接差し替えると同時に osSandboxEnabled の
	// 判定はプラットフォーム依存のため、テストは wrapFn 経由のみで検証する。
	wrapCalled := false
	sh.wrapFn = func(_ context.Context, _ []string, _ string, _ []string) (*exec.Cmd, error) {
		wrapCalled = true
		return nil, errors.New("sandbox-exec not found")
	}
	// osSandboxEnabled("auto") が false な環境 (darwin 以外) ではラップ経路自体に
	// 入らないため、この単体テストは darwin ビルドでのみ意味を持つ。それ以外では
	// wrapFn が呼ばれないことを許容し、Execute が通常どおり成功することのみ確認する。
	res, err := sh.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["hi"]}`))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if osSandboxPlatformSupported() {
		if !wrapCalled {
			t.Fatal("darwin では wrapFn が呼ばれること")
		}
		if !res.IsError {
			t.Fatal("wrapFn 失敗時は IsError:true")
		}
		if !strings.Contains(buf.String(), "os_sandbox_wrap_failed") {
			t.Fatalf("audit ログに os_sandbox_wrap_failed: %s", buf.String())
		}
		if strings.Contains(res.Content, "hi") {
			t.Fatal("コマンドが実行されてはいけない (フェイルオープン禁止)")
		}
	} else {
		if wrapCalled {
			t.Fatal("非 darwin では wrapFn が呼ばれてはいけない")
		}
		if res.IsError {
			t.Fatalf("非 darwin では通常実行される: %s", res.Content)
		}
	}
}

// TestShell_OSSandboxOff off 指定では wrapFn が一度も呼ばれない
func TestShell_OSSandboxOff(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sh := NewShell(config.ShellToolConfig{
		TimeoutSeconds: 5, MaxTimeoutSeconds: 5,
		AllowBinaries: []string{"echo"},
		OSSandbox:     "off",
	}, logger, nil)
	wrapCalled := false
	sh.wrapFn = func(_ context.Context, _ []string, _ string, _ []string) (*exec.Cmd, error) {
		wrapCalled = true
		return nil, errors.New("should not be called")
	}
	res, err := sh.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["hi"]}`))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if wrapCalled {
		t.Fatal("os_sandbox: off では wrapFn が呼ばれてはいけない")
	}
	if res.IsError || !strings.Contains(res.Content, "hi") {
		t.Fatalf("off では通常実行される: %+v", res)
	}
}

// TestShell_NilWrapFnFallsBackToPackageFunc struct literal で組み立てた
// ShellTool でも wrapFn が nil のとき panic せずパッケージ関数へフォールバックする
func TestShell_NilWrapFnFallsBackToPackageFunc(t *testing.T) {
	sh := &ShellTool{
		cfg:    config.ShellToolConfig{TimeoutSeconds: 5, MaxTimeoutSeconds: 5, AllowBinaries: []string{"echo"}, OSSandbox: "auto"},
		allow:  map[string]struct{}{"echo": {}},
		logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	// panic しないことのみを確認する。darwin では sandbox-exec があれば成功、
	// 無ければ IsError。非 darwin では通常実行される。いずれも panic なしで戻ること。
	_, err := sh.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["hi"]}`))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
}
