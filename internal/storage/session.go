package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry セッション履歴 1 行分
type Entry struct {
	Time    time.Time       `json:"time"`
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

// SessionStore JSONL でセッションを永続化する
type SessionStore interface {
	Append(ctx context.Context, sessionID string, e Entry) error
	Read(ctx context.Context, sessionID string) ([]Entry, error)
}

type sessionStore struct {
	dir string
	mu  sync.Mutex
}

// NewSessionStore ディレクトリを指定して SessionStore を生成する
func NewSessionStore(dir string) SessionStore {
	return &sessionStore{dir: dir}
}

func (s *sessionStore) path(id string) string {
	return filepath.Join(s.dir, id+".jsonl")
}

// Append セッションファイルに 1 行追記する
func (s *sessionStore) Append(_ context.Context, id string, e Entry) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("storage mkdir: %w", err)
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	f, err := os.OpenFile(s.path(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("storage open: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("storage marshal: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("storage write: %w", err)
	}
	return nil
}

// Read セッションファイル全行を読む
func (s *sessionStore) Read(_ context.Context, id string) ([]Entry, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return nil, fmt.Errorf("storage open: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var entries []Entry
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("storage parse: %w", err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("storage scan: %w", err)
	}
	return entries, nil
}

// ErrEmpty 既存セッションが空
var ErrEmpty = errors.New("storage: empty session")
