// Package memory 短期/長期メモリと note 保存・検索を提供する
// 11 番設計書の MVP として JSONL ベースの全文検索を実装し、後で SQLite FTS5 や
// ベクター DB に差し替え可能なインターフェースを介す
package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
// Add は Lock を取り、Search は RLock で並行 reader を許容する
type fileNoteStore struct {
	path string
	mu   sync.RWMutex
}

// NewFileNoteStore 指定パスに JSONL でノートを永続化する Store を返す
// 親ディレクトリは自動で 0o700 で作成する
func NewFileNoteStore(path string) (NoteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("notes mkdir: %w", err)
	}
	return &fileNoteStore{path: path}, nil
}

// maxJSONLRecordBytes Add で許容する 1 レコードの最大バイト数
// Search の bufio.Scanner バッファ上限 (16 MiB) を超えるレコードを書くと、
// それ以降の全 Search が scanner error で破綻するため、書き込み時点で同じ上限を強制する
// 末尾改行を含めたサイズで比較する
const maxJSONLRecordBytes = 16 * 1024 * 1024

// ErrNoteTooLarge Add で 1 レコードのサイズが maxJSONLRecordBytes を超えた場合に返す
var ErrNoteTooLarge = fmt.Errorf("notes: record exceeds %d bytes", maxJSONLRecordBytes)

// Add ノートを追加する。ID 未指定の場合は UUID v4 を生成する
// 1 レコードの JSON 表現が maxJSONLRecordBytes を超える場合は ErrNoteTooLarge を返す
func (s *fileNoteStore) Add(_ context.Context, n Note) (Note, error) {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	b, err := json.Marshal(n)
	if err != nil {
		return Note{}, fmt.Errorf("notes marshal: %w", err)
	}
	// +1 は末尾改行の分
	if len(b)+1 > maxJSONLRecordBytes {
		return Note{}, fmt.Errorf("%w (got %d)", ErrNoteTooLarge, len(b)+1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Note{}, fmt.Errorf("notes open: %w", err)
	}
	defer func() { _ = f.Close() }()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	// LLM が生成する長文ノートを 1 レコードとして格納するため、初期 64KiB から
	// 最大 16MiB まで拡張する (internal/mcp/client.go のスキャナと同水準)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	// 行番号で問題箇所を特定できるよう lineIdx を回す
	lineIdx := 0
	for scanner.Scan() {
		lineIdx++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var n Note
		if err := json.Unmarshal(line, &n); err != nil {
			// 単一の壊れた JSONL 行で全検索を中断しない。問題行を warn ログに記録するが、
			// err には raw line の断片やノート本文の機密情報が含まれ得るため、
			// 詳細を伏せて record の位置 (1-origin の行番号) と byte 長だけを記録する
			slog.Warn("notes: skipping malformed JSONL line", "line", lineIdx, "bytes", len(line))
			continue
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

// tokenize 検索語を小文字化して分割する
// ASCII のみのトークンは空白・記号で分割した語をそのまま採用する
// 非 ASCII 文字 (CJK など) を含むトークンは分かち書きが期待できないため rune 2-gram に展開し、
// scoreNote の strings.Contains による部分一致で日本語ノートも検索可能にする
// MVP 段階の実装で、より高品質な検索が必要な場合は SQLite FTS5 やベクター DB へ差し替える
func tokenize(q string) []string {
	q = strings.ToLower(q)
	raw := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' || r == '!' || r == '?'
	})
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		if tok == "" {
			continue
		}
		if isASCIIOnly(tok) {
			out = append(out, tok)
			continue
		}
		runes := []rune(tok)
		if len(runes) == 1 {
			out = append(out, tok)
			continue
		}
		// 非 ASCII を含むトークンを長さ 2 の rune 連結で展開する
		// 例: 「メモリ管理」 → ["メモ", "モリ", "リ管", "管理"]
		for i := 0; i+1 < len(runes); i++ {
			out = append(out, string(runes[i:i+2]))
		}
	}
	return out
}

// isASCIIOnly s が全て ASCII 範囲の文字で構成されているかを判定する
// utf8.RuneSelf (=128) 未満のバイトのみで埋まっていれば真
func isASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
