package billing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileStore JSONL 形式で Snapshot を永続化するファイルベース Store
type FileStore struct {
	path string
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
}

// NewFileStore 指定パスに JSONL を追記する Store を構築する
// 親ディレクトリは自動で 0o700 で作成する
func NewFileStore(path string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("billing mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("billing open: %w", err)
	}
	return &FileStore{path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// Close 内部バッファを flush してファイルを閉じる
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return fmt.Errorf("billing flush: %w", err)
		}
	}
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}

// Append Snapshot を JSON 1 行として末尾に追記し、必ず flush する
func (s *FileStore) Append(_ context.Context, snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("billing marshal: %w", err)
	}
	if _, err := s.w.Write(b); err != nil {
		return fmt.Errorf("billing write: %w", err)
	}
	if err := s.w.WriteByte('\n'); err != nil {
		return fmt.Errorf("billing newline: %w", err)
	}
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("billing flush: %w", err)
	}
	return nil
}

// QuerySession 指定 session_id に紐づく Snapshot を時系列順で返す
func (s *FileStore) QuerySession(_ context.Context, sessionID string) ([]Snapshot, error) {
	return s.queryAll(func(snap Snapshot) bool { return snap.SessionID == sessionID })
}

// QueryDate 指定日付 (UTC, YYYY-MM-DD) の Snapshot を返す
func (s *FileStore) QueryDate(_ context.Context, date string) ([]Snapshot, error) {
	return s.queryAll(func(snap Snapshot) bool { return snap.At.UTC().Format("2006-01-02") == date })
}

// queryAll ファイル全体を走査し predicate にマッチした Snapshot を返す
// 実装は線形スキャン O(n) のため、本番環境で大量のレコードが蓄積するケースには不向き
// 大規模運用では SQLite など index 付きストレージに置き換える前提の MVP 実装
func (s *FileStore) queryAll(pred func(Snapshot) bool) ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return nil, fmt.Errorf("billing flush: %w", err)
		}
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("billing query open: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []Snapshot
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var snap Snapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			return nil, fmt.Errorf("billing unmarshal: %w", err)
		}
		if pred(snap) {
			out = append(out, snap)
		}
	}
	if err := scanner.Err(); err != nil {
		// bufio.Scanner は EOF を err として返さないため err != nil は実エラーのみ
		return nil, fmt.Errorf("billing scan: %w", err)
	}
	return out, nil
}
