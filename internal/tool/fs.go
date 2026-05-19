package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const defaultMaxRead = 1 << 20

// FSRead fs_read ツールの実装
type FSRead struct {
	sb       *Sandbox
	maxBytes int
}

// NewFSRead Sandbox と最大バイト数で FSRead を生成する
func NewFSRead(sb *Sandbox, maxBytes int) *FSRead {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRead
	}
	return &FSRead{sb: sb, maxBytes: maxBytes}
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
func (t *FSRead) Execute(_ context.Context, raw json.RawMessage) (Result, error) {
	var a fsReadArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{IsError: true, Content: "path is required"}, nil
	}
	if err := t.sb.CheckPath(a.Path); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = f.Close() }()
	limited := io.LimitReader(f, int64(t.maxBytes)+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	truncated := false
	if len(b) > t.maxBytes {
		b = b[:t.maxBytes]
		truncated = true
	}
	return Result{Content: string(b), Truncated: truncated}, nil
}

// FSWrite fs_write ツールの実装
type FSWrite struct {
	sb *Sandbox
}

// NewFSWrite Sandbox から FSWrite を生成する
func NewFSWrite(sb *Sandbox) *FSWrite {
	return &FSWrite{sb: sb}
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
func (t *FSWrite) Execute(_ context.Context, raw json.RawMessage) (Result, error) {
	var a fsWriteArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Path == "" {
		return Result{IsError: true, Content: "path is required"}, nil
	}
	if err := t.sb.CheckPath(a.Path); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if err := os.WriteFile(a.Path, []byte(a.Content), 0o600); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)}, nil
}
