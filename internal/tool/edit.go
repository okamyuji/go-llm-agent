package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// FSEdit fs_edit ツールの実装。old_string の完全一致置換のみを行う
type FSEdit struct {
	sb       *Sandbox
	registry *ReadRegistry
	logger   *slog.Logger
}

// NewFSEdit Sandbox と read registry と logger から FSEdit を生成する。
// registry が nil の場合、全ての fs_edit が「未読」として拒否される
func NewFSEdit(sb *Sandbox, registry *ReadRegistry, logger *slog.Logger) *FSEdit {
	return &FSEdit{sb: sb, registry: registry, logger: logger}
}

// Spec ツール定義を返す
func (t *FSEdit) Spec() Spec {
	return Spec{
		Name: "fs_edit",
		Description: "Replace an exact substring in a file. old_string must match the " +
			"file content exactly. If replace_all is false (default), old_string must " +
			"match exactly once, or the call fails. Requires the file to have been read " +
			"first with fs_read or fs_write in this session.",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
"path":{"type":"string","description":"absolute or relative path, must be read first"},
"old_string":{"type":"string","description":"exact substring to find"},
"new_string":{"type":"string","description":"replacement text"},
"replace_all":{"type":"boolean","description":"replace every match, default false"}
},
"required":["path","old_string","new_string"]
}`),
	}
}

type fsEditArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// validateEditArgs a の必須フィールドと自明な矛盾 (old==new) を検査する。
// 問題なければ nil を返す
func validateEditArgs(a fsEditArgs) *Result {
	if a.Path == "" {
		return &Result{IsError: true, Content: "path is required"}
	}
	if a.OldString == "" {
		return &Result{IsError: true, Content: "old_string is required"}
	}
	if a.OldString == a.NewString {
		return &Result{IsError: true, Content: "old_string and new_string are identical, nothing to do"}
	}
	return nil
}

// Execute path のパス検証・既読チェック・一致件数検査を経て置換を行う
func (t *FSEdit) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a fsEditArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if bad := validateEditArgs(a); bad != nil {
		return *bad, nil
	}

	root, relative, err := t.sb.openRootForPath(a.Path)
	if err != nil {
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = root.Close() }()

	canonical, err := t.sb.resolveCanonical(a.Path)
	if err != nil {
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if !t.registry.isKnown(canonical) {
		msg := fmt.Sprintf("fs_edit: %q was not read in this session; call fs_read first", a.Path)
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	}

	if info, lerr := root.Lstat(relative); lerr != nil {
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, lerr.Error())
		return Result{IsError: true, Content: lerr.Error()}, nil
	} else if info.Mode()&os.ModeSymlink != 0 {
		msg := fmt.Sprintf("sandbox: symlink 経由のアクセスは拒否 %q", a.Path)
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	}

	b, err := root.ReadFile(relative)
	if err != nil {
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	content := string(b)

	count := strings.Count(content, a.OldString)
	if count == 0 {
		msg := fmt.Sprintf("fs_edit: old_string not found in %s", a.Path)
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	}
	if !a.ReplaceAll && count > 1 {
		msg := fmt.Sprintf("fs_edit: old_string matched %d times in %s, expected exactly 1; "+
			"pass replace_all=true or narrow old_string to a unique match", count, a.Path)
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	}

	var replaced string
	if a.ReplaceAll {
		replaced = strings.ReplaceAll(content, a.OldString, a.NewString)
	} else {
		replaced = strings.Replace(content, a.OldString, a.NewString, 1)
	}

	if err := root.WriteFile(relative, []byte(replaced), 0o600); err != nil {
		auditFS(ctx, t.logger, "fs_edit", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	t.registry.markKnown(canonical)
	auditFS(ctx, t.logger, "fs_edit", a.Path, len(replaced), true, fmt.Sprintf("ok replaced=%d", count))
	return Result{Content: fmt.Sprintf("replaced %d occurrence(s) in %s", count, a.Path)}, nil
}
