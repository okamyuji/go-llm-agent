package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const defaultMaxRead = 1 << 20

// SummaryReader 承認サマリ生成のための読み取り専用インターフェース。
// ReadForSummary は Sandbox のパス検証を通すが read registry を更新しない
type SummaryReader interface {
	ReadForSummary(ctx context.Context, path string) (string, error)
}

// FSRead fs_read ツールの実装
type FSRead struct {
	sb       *Sandbox
	maxBytes int
	logger   *slog.Logger
	registry *ReadRegistry
}

// NewFSRead Sandbox と最大バイト数で FSRead を生成する。read registry は
// 空のものを内部で生成する (fs_edit の既読チェックを共有したい場合は
// NewFSReadWithLogger へ共有インスタンスを渡すこと)
func NewFSRead(sb *Sandbox, maxBytes int) *FSRead {
	return NewFSReadWithLogger(sb, maxBytes, nil, NewReadRegistry())
}

// NewFSReadWithLogger Sandbox と最大バイト数と logger と read registry で FSRead を生成する
func NewFSReadWithLogger(sb *Sandbox, maxBytes int, logger *slog.Logger, registry *ReadRegistry) *FSRead {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRead
	}
	return &FSRead{sb: sb, maxBytes: maxBytes, logger: logger, registry: registry}
}

// Spec ツール定義を返す
func (t *FSRead) Spec() Spec {
	return Spec{
		Name:        "fs_read",
		Description: "ローカルファイルを読みテキストを返す。サンドボックス配下のパスのみ許可",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{"path":{"type":"string","description":"絶対または相対パス"}},
"required":["path"]
}`),
	}
}

type fsReadArgs struct {
	Path string `json:"path"`
}

// Execute 引数からパスを受け取り読み込みを行う
func (t *FSRead) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a fsReadArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{IsError: true, Content: "path is required"}, nil
	}
	root, relative, err := t.sb.openRootForPath(a.Path)
	if err != nil {
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = root.Close() }()
	if info, lerr := root.Lstat(relative); lerr != nil {
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, lerr.Error())
		return Result{IsError: true, Content: lerr.Error()}, nil
	} else if info.Mode()&os.ModeSymlink != 0 {
		msg := fmt.Sprintf("sandbox: symlink 経由のアクセスは拒否 %q", a.Path)
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	}
	f, err := root.Open(relative)
	if err != nil {
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = f.Close() }()
	limited := io.LimitReader(f, int64(t.maxBytes)+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	truncated := false
	if len(b) > t.maxBytes {
		b = b[:t.maxBytes]
		truncated = true
	}
	auditFS(ctx, t.logger, "fs_read", a.Path, len(b), true, "ok")
	if canonical, cerr := t.sb.resolveCanonical(a.Path); cerr == nil {
		t.registry.markKnown(canonical)
	}
	return Result{Content: string(b), Truncated: truncated}, nil
}

// ReadForSummary path を Sandbox 経由で読み、内容を返す。
// Execute と異なり markKnown を呼ばないため、承認サマリ生成のための読み取りが
// fs_edit の既読チェックを汚さない
func (t *FSRead) ReadForSummary(_ context.Context, path string) (string, error) {
	root, relative, err := t.sb.openRootForPath(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if info, lerr := root.Lstat(relative); lerr != nil {
		return "", lerr
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("sandbox: symlink 経由のアクセスは拒否 %q", path)
	}
	f, err := root.Open(relative)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	limited := io.LimitReader(f, int64(t.maxBytes)+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if len(b) > t.maxBytes {
		b = b[:t.maxBytes]
	}
	return string(b), nil
}

// FSWrite fs_write ツールの実装
type FSWrite struct {
	sb       *Sandbox
	logger   *slog.Logger
	registry *ReadRegistry
}

// NewFSWrite Sandbox から FSWrite を生成する。read registry は空のものを内部で生成する
func NewFSWrite(sb *Sandbox) *FSWrite {
	return NewFSWriteWithLogger(sb, nil, NewReadRegistry())
}

// NewFSWriteWithLogger Sandbox と logger と read registry から FSWrite を生成する
func NewFSWriteWithLogger(sb *Sandbox, logger *slog.Logger, registry *ReadRegistry) *FSWrite {
	return &FSWrite{sb: sb, logger: logger, registry: registry}
}

// Spec ツール定義を返す
func (t *FSWrite) Spec() Spec {
	return Spec{
		Name:        "fs_write",
		Description: "テキストをファイルに書き込む。サンドボックス配下のみ。親ディレクトリは自動作成",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
"path":{"type":"string"},
"content":{"type":"string"}
},
"required":["path","content"]
}`),
	}
}

type fsWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Execute 引数からパスと内容を受け取り書き込みを行う
func (t *FSWrite) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a fsWriteArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{IsError: true, Content: "path is required"}, nil
	}
	root, relative, err := t.sb.openRootForPath(a.Path)
	if err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = root.Close() }()
	if info, lerr := root.Lstat(relative); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		msg := fmt.Sprintf("sandbox: 既存パスが symlink のため拒否 %q", a.Path)
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, msg)
		return Result{IsError: true, Content: msg}, nil
	} else if lerr != nil && !os.IsNotExist(lerr) {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, lerr.Error())
		return Result{IsError: true, Content: lerr.Error()}, nil
	}
	if err := root.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if err := root.WriteFile(relative, []byte(a.Content), 0o600); err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	auditFS(ctx, t.logger, "fs_write", a.Path, len(a.Content), true, "ok")
	if canonical, cerr := t.sb.resolveCanonical(a.Path); cerr == nil {
		t.registry.markKnown(canonical)
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)}, nil
}

func auditFS(ctx context.Context, logger *slog.Logger, op, path string, bytesLen int, ok bool, reason string) {
	if logger == nil {
		return
	}
	corr := ""
	if ctx != nil {
		if v, ok2 := ctx.Value(CorrelationKey()).(string); ok2 {
			corr = v
		}
	}
	logger.Info("audit",
		slog.String("tool", op),
		slog.String("path", path),
		slog.Int("bytes", bytesLen),
		slog.Bool("ok", ok),
		slog.String("reason", reason),
		slog.String("correlation_id", corr),
	)
}
