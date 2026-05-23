// Package memory 短期/長期メモリと note 保存・検索を提供する
// 11 番設計書の MVP として JSONL ベースの全文検索を実装し、後で SQLite FTS5 や
// ベクター DB に差し替え可能なインターフェースを介す
package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Note 保存される 1 件のノート
type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// NoteStore ノートの保存と検索インターフェース
type NoteStore interface {
	Add(ctx context.Context, n Note) (Note, error)
	Search(ctx context.Context, query string, topK int) ([]Note, error)
}

// fileNoteStore JSONL ベースの簡易 NoteStore
type fileNoteStore struct {
	path string
	mu   sync.Mutex
}

// NewFileNoteStore 指定パスに JSONL でノートを永続化する Store を返す
// 親ディレクトリは自動で 0o700 で作成する
func NewFileNoteStore(path string) (NoteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("notes mkdir: %w", err)
	}
	return &fileNoteStore{path: path}, nil
}

// Add ノートを追加する。ID 未指定の場合は UUID v4 を生成する
func (s *fileNoteStore) Add(_ context.Context, n Note) (Note, error) {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Note{}, fmt.Errorf("notes open: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(n)
	if err != nil {
		return Note{}, fmt.Errorf("notes marshal: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return Note{}, fmt.Errorf("notes write: %w", err)
	}
	return n, nil
}

// maxTopK Search の topK パラメータの上限
// 攻撃者が極端に大きな topK (例: 10_000_000) を渡してメモリ枯渇を起こすことを防ぐ
const maxTopK = 100

// Search 単純な単語マッチで上位 topK を返す
// スコアは title/body/tags の重複した検索語数（重み: title=3, tags=2, body=1）
// topK は maxTopK でクランプする
func (s *fileNoteStore) Search(_ context.Context, query string, topK int) ([]Note, error) {
	if topK <= 0 {
		topK = 5
	}
	if topK > maxTopK {
		topK = maxTopK
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("notes open: %w", err)
	}
	defer func() { _ = f.Close() }()

	terms := tokenize(query)
	if len(terms) == 0 {
		return nil, nil
	}
	type scored struct {
		n     Note
		score int
	}
	var ranked []scored
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var n Note
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, fmt.Errorf("notes unmarshal: %w", err)
		}
		score := scoreNote(n, terms)
		if score > 0 {
			ranked = append(ranked, scored{n: n, score: score})
		}
	}
	if err := scanner.Err(); err != nil {
		// bufio.Scanner は EOF を err として返さないため、err != nil は実エラーのみ
		return nil, fmt.Errorf("notes scan: %w", err)
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	out := make([]Note, len(ranked))
	for i, r := range ranked {
		out[i] = r.n
	}
	return out, nil
}

// scoreNote n が terms をどれくらい含むかをスコア化する
func scoreNote(n Note, terms []string) int {
	title := strings.ToLower(n.Title)
	body := strings.ToLower(n.Body)
	tags := strings.ToLower(strings.Join(n.Tags, " "))
	score := 0
	for _, t := range terms {
		if t == "" {
			continue
		}
		if strings.Contains(title, t) {
			score += 3
		}
		if strings.Contains(tags, t) {
			score += 2
		}
		if strings.Contains(body, t) {
			score++
		}
	}
	return score
}

// tokenize 検索語を小文字化して空白で分割する
func tokenize(q string) []string {
	q = strings.ToLower(q)
	raw := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' || r == '!' || r == '?'
	})
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}
