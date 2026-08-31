package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type walFile struct {
	mu   sync.Mutex
	path string
	seq  uint64
}

func walPath(dir, sessionID, runID string) string {
	return filepath.Join(dir, sessionID, runID+".jsonl")
}

func cursorPath(dir, sessionID, runID string) string {
	return filepath.Join(dir, sessionID, runID+".cursor")
}

func lockPath(dir, runID string) string {
	return filepath.Join(dir, runID+".lock")
}

func openWAL(dir, sessionID, runID string) (*walFile, error) {
	if err := os.MkdirAll(filepath.Join(dir, sessionID), 0o700); err != nil {
		return nil, err
	}
	p := walPath(dir, sessionID, runID)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return &walFile{path: p}, nil
}

// Append Seq を振って 1 行追記する。O_APPEND で開き直し、mutex で直列化する
func (w *walFile) Append(e Event) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.Seq = w.seq
	line, err := e.Marshal()
	if err != nil {
		return Event{}, err
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Event{}, err
	}
	w.seq++
	return e, nil
}

func readCursor(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// writeCursor 一時ファイルに書いて rename する。途中で落ちても古い値か新しい値のどちらかが残る
func writeCursor(path string, off int64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(off, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type walRecord struct {
	Line []byte
	End  int64
}

// readFrom off から最大 max 行を読む。改行で終わらない末尾行と JSON 不正行は捨てる
func readFrom(path string, off int64, max int) ([]walRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	var out []walRecord
	pos := off
	for len(out) < max {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				slog.Warn("audit wal: dropping partial tail line", "path", path, "bytes", len(line))
			}
			break
		}
		if err != nil {
			return out, err
		}
		pos += int64(len(line))
		body := bytes.TrimRight(line, "\n")
		if !json.Valid(body) {
			slog.Warn("audit wal: dropping invalid json line", "path", path, "offset", pos)
			continue
		}
		out = append(out, walRecord{Line: body, End: pos})
	}
	return out, nil
}

func flockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return f, nil
}

// acquireRunLock 自分の run の lock を取る。プロセスが死ねば OS が外す
func acquireRunLock(dir, runID string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return flockFile(lockPath(dir, runID))
}

// tryLockRun 他 run の lock を試す。取れたらその run は死んでいる
func tryLockRun(dir, runID string) (*os.File, bool) {
	f, err := flockFile(lockPath(dir, runID))
	if err != nil {
		return nil, false
	}
	return f, true
}
