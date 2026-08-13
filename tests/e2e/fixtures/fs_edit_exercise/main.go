// Package main 03-fs-edit.md の fs_edit / read registry を検証するフィクスチャ。
// internal/tool を直接呼び出し、結果を key=value 形式で標準出力へ書く
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func editArgs(path, oldStr, newStr string, replaceAll bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"path": path, "old_string": oldStr, "new_string": newStr, "replace_all": replaceAll,
	})
	return b
}

func readArgs(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"path": path})
	return b
}

// exerciseResult main が検証する各シナリオの成否
type exerciseResult struct {
	editBeforeReadDenied  bool
	editAfterReadOK       bool
	onlyTargetLineChanged bool
	ambiguousMatchDenied  bool
}

// runEditBeforeRead fs_read を経由しない fs_edit が拒否されることを確認する。
// fs_write は registry に記録するため、既に書込み済みの reg とは別の
// (未読状態を再現する) registry を使う
func runEditBeforeRead(ctx context.Context, sb *tool.Sandbox, target string) bool {
	freshReg := tool.NewReadRegistry()
	freshEdit := tool.NewFSEdit(sb, freshReg, nil)
	res, _ := freshEdit.Execute(ctx, editArgs(target, "change me", "changed", false))
	return res.IsError && strings.Contains(res.Content, "was not read in this session")
}

// runEditAfterRead fs_read の後に fs_edit が成功し、対象行だけが変わることを確認する
func runEditAfterRead(ctx context.Context, r *tool.FSRead, e *tool.FSEdit, target, want string) (editOK, onlyTargetChanged bool) {
	if rres, _ := r.Execute(ctx, readArgs(target)); rres.IsError {
		return false, false
	}
	afterReadRes, _ := e.Execute(ctx, editArgs(target, "change me", "changed", false))
	editOK = !afterReadRes.IsError

	rres2, _ := r.Execute(ctx, readArgs(target))
	onlyTargetChanged = rres2.Content == want
	return editOK, onlyTargetChanged
}

// runAmbiguousMatch 一致が複数件あるファイルへ replace_all=false で fs_edit すると
// 拒否されることを確認する
func runAmbiguousMatch(ctx context.Context, sb *tool.Sandbox, reg *tool.ReadRegistry, dir string) (bool, error) {
	w := tool.NewFSWriteWithLogger(sb, nil, reg)
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	e := tool.NewFSEdit(sb, reg, nil)

	ambiguous := dir + "/ambiguous.txt"
	if wres, _ := w.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"content":%q}`, ambiguous, "dup dup"))); wres.IsError {
		return false, fmt.Errorf("ambiguous write: %s", wres.Content)
	}
	if rres, _ := r.Execute(ctx, readArgs(ambiguous)); rres.IsError {
		return false, fmt.Errorf("ambiguous read: %s", rres.Content)
	}
	ambRes, _ := e.Execute(ctx, editArgs(ambiguous, "dup", "x", false))
	return ambRes.IsError && strings.Contains(ambRes.Content, "matched 2 times"), nil
}

// runExercise 一時ディレクトリを sandbox にして一連のシナリオを実行する
func runExercise(dir string) (exerciseResult, error) {
	ctx := context.Background()
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	w := tool.NewFSWriteWithLogger(sb, nil, reg)
	e := tool.NewFSEdit(sb, reg, nil)

	target := dir + "/target.txt"
	original := "line1: keep\nline2: change me\nline3: keep\n"
	if wres, _ := w.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"path":%q,"content":%q}`, target, original))); wres.IsError {
		return exerciseResult{}, fmt.Errorf("initial write: %s", wres.Content)
	}

	var res exerciseResult
	res.editBeforeReadDenied = runEditBeforeRead(ctx, sb, target)

	want := "line1: keep\nline2: changed\nline3: keep\n"
	res.editAfterReadOK, res.onlyTargetLineChanged = runEditAfterRead(ctx, r, e, target, want)

	ambiguousDenied, err := runAmbiguousMatch(ctx, sb, reg, dir)
	if err != nil {
		return exerciseResult{}, err
	}
	res.ambiguousMatchDenied = ambiguousDenied

	return res, nil
}

func main() {
	dir, err := os.MkdirTemp("", "fs_edit_exercise")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR mkdtemp:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	res, err := runExercise(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(2)
	}
	fmt.Printf("edit_before_read_denied=%v\n", res.editBeforeReadDenied)
	fmt.Printf("edit_after_read_ok=%v\n", res.editAfterReadOK)
	fmt.Printf("only_target_line_changed=%v\n", res.onlyTargetLineChanged)
	fmt.Printf("ambiguous_match_denied=%v\n", res.ambiguousMatchDenied)
}
