package tool

import (
	"path/filepath"
	"sync"
)

// ReadRegistry は fs_edit の前提となる「内容既知」パス集合を管理する。
// fs_read の成功時と fs_write の成功時に記録する。fs_write も記録するのは、
// 書き込んだ内容をプロセスが直前に確定させており、その時点で内容が既知だからである。
// 公開型にするのは、NewReadRegistry・NewFSEdit・NewFSReadWithLogger・
// NewFSWriteWithLogger がいずれも公開関数でこの型を受け渡すためである
// (revive の unexported-return に抵触させない)。
type ReadRegistry struct {
	mu    sync.Mutex
	known map[string]struct{}
}

// NewReadRegistry 空の ReadRegistry を生成する
func NewReadRegistry() *ReadRegistry {
	return &ReadRegistry{known: map[string]struct{}{}}
}

// markKnown root からの絶対パスを正規化し記録する。r が nil の場合は何もしない
func (r *ReadRegistry) markKnown(absPath string) {
	if r == nil {
		return
	}
	key := filepath.Clean(absPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.known[key] = struct{}{}
}

// isKnown absPath が記録済みかを返す。r が nil の場合は常に false を返す
func (r *ReadRegistry) isKnown(absPath string) bool {
	if r == nil {
		return false
	}
	key := filepath.Clean(absPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.known[key]
	return ok
}
