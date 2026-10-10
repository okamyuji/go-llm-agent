package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain テストが利用者のホームへ監査 WAL や自動メモリを書かないよう、HOME を一時ディレクトリにし IGGY_PAT を外す。
// IGGY_PAT が設定された開発機では、runOneShot を通るテストが ~/.go-llm-agent/audit-wal に run-* を残し続けるため
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) (code int) {
	// HOME を差し替えると、テストが起動する go build の GOPATH / GOMODCACHE / GOCACHE / GOENV が
	// 一時 HOME 配下へ移り、読み取り専用のモジュールキャッシュを毎回ダウンロードして RemoveAll できなくなる。
	// 差し替える前の値を固定し、一時 HOME を見るのは agent 本体だけにする
	if err := pinGoEnv(); err != nil {
		// go が無い環境でも go build を使わないテストは動かせるので、警告だけにする
		fmt.Fprintln(os.Stderr, "TestMain: Go のキャッシュ場所を固定できません:", err)
	}
	home, err := os.MkdirTemp("", "agent-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(home); err != nil {
			fmt.Fprintln(os.Stderr, "TestMain: remove temp HOME:", err)
			code = 1
		}
	}()
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	if err := os.Unsetenv("IGGY_PAT"); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	return m.Run()
}

// pinGoEnv 現在の HOME から決まる Go のキャッシュと設定の場所を環境変数に固定する
func pinGoEnv() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	var vals map[string]string
	if err := json.Unmarshal(out, &vals); err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	for k, v := range vals {
		if v == "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
