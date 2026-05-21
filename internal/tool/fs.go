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

// FSRead fs_read ツールの実装
type FSRead struct {
	sb       *Sandbox
	maxBytes int
	logger   *slog.Logger
}

// NewFSRead Sandbox と最大バイト数で FSRead を生成する
func NewFSRead(sb *Sandbox, maxBytes int) *FSRead {
	return NewFSReadWithLogger(sb, maxBytes, nil)
}

// NewFSReadWithLogger Sandbox と最大バイト数と logger で FSRead を生成する
func NewFSReadWithLogger(sb *Sandbox, maxBytes int, logger *slog.Logger) *FSRead {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRead
	}
	return &FSRead{sb: sb, maxBytes: maxBytes, logger: logger}
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
	if err := t.sb.CheckPath(a.Path); err != nil {
		auditFS(ctx, t.logger, "fs_read", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	f, err := os.Open(a.Path)
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
	return Result{Content: string(b), Truncated: truncated}, nil
}

// FSWrite fs_write ツールの実装
type FSWrite struct {
	sb     *Sandbox
	logger *slog.Logger
}

// NewFSWrite Sandbox から FSWrite を生成する
func NewFSWrite(sb *Sandbox) *FSWrite {
	return NewFSWriteWithLogger(sb, nil)
}

// NewFSWriteWithLogger Sandbox と logger から FSWrite を生成する
func NewFSWriteWithLogger(sb *Sandbox, logger *slog.Logger) *FSWrite {
	return &FSWrite{sb: sb, logger: logger}
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
	if err := t.sb.CheckPath(a.Path); err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if err := os.WriteFile(a.Path, []byte(a.Content), 0o600); err != nil {
		auditFS(ctx, t.logger, "fs_write", a.Path, 0, false, err.Error())
		return Result{IsError: true, Content: err.Error()}, nil
	}
	auditFS(ctx, t.logger, "fs_write", a.Path, len(a.Content), true, "ok")
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)}, nil
}

func auditFS(ctx context.Context, logger *slog.Logger, op, path string, bytesLen int, ok bool, reason string) {
	if logger == nil {
		return
	}
	corr, _ := ctx.Value(correlationKey{}).(string)
	logger.Info("audit",
		slog.String("tool", op),
		slog.String("path", path),
		slog.Int("bytes", bytesLen),
		slog.Bool("ok", ok),
		slog.String("reason", reason),
		slog.String("correlation_id", corr),
	)
}
