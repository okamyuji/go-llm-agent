package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestEvent(kind Kind) Event {
	return Event{V: 1, ID: "id", SessionID: "s", RunID: "r", TS: time.Now().UTC(), Kind: kind, Payload: json.RawMessage(`{}`)}
}

func TestWALAppendAssignsSeqAndPersists(t *testing.T) {
	dir := t.TempDir()
	w, err := openWAL(dir, "s", "r")
	if err != nil {
		t.Fatal(err)
	}
	e1, _ := w.Append(newTestEvent(KindUsage))
	e2, _ := w.Append(newTestEvent(KindUsage))
	if e1.Seq != 0 || e2.Seq != 1 {
		t.Fatalf("seq = %d,%d", e1.Seq, e2.Seq)
	}
	recs, err := readFrom(walPath(dir, "s", "r"), 0, 100)
	if err != nil || len(recs) != 2 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	info, _ := os.Stat(walPath(dir, "s", "r"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(filepath.Join(dir, "s"))
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o", dinfo.Mode().Perm())
	}
}

func TestWALConcurrentAppendKeepsLinesIntact(t *testing.T) {
	dir := t.TempDir()
	w, _ := openWAL(dir, "s", "r")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := newTestEvent(KindUsage)
			e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":2}`)
			_, _ = w.Append(e)
		}()
	}
	wg.Wait()
	recs, err := readFrom(walPath(dir, "s", "r"), 0, 1000)
	if err != nil || len(recs) != 50 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	for _, r := range recs {
		var e Event
		if err := json.Unmarshal(r.Line, &e); err != nil {
			t.Fatalf("broken line: %s", r.Line)
		}
	}
}

func TestReadFromDropsPartialTail(t *testing.T) {
	dir := t.TempDir()
	w, _ := openWAL(dir, "s", "r")
	_, _ = w.Append(newTestEvent(KindUsage))
	f, _ := os.OpenFile(walPath(dir, "s", "r"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"v":1,"id":"partial`)
	_ = f.Close()
	recs, err := readFrom(walPath(dir, "s", "r"), 0, 100)
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
}

func TestReadFromResumesAtOffset(t *testing.T) {
	dir := t.TempDir()
	w, _ := openWAL(dir, "s", "r")
	_, _ = w.Append(newTestEvent(KindUsage))
	_, _ = w.Append(newTestEvent(KindUsage))
	first, _ := readFrom(walPath(dir, "s", "r"), 0, 1)
	rest, _ := readFrom(walPath(dir, "s", "r"), first[0].End, 10)
	if len(rest) != 1 {
		t.Fatalf("rest=%d", len(rest))
	}
	var e Event
	_ = json.Unmarshal(rest[0].Line, &e)
	if e.Seq != 1 {
		t.Fatalf("seq=%d", e.Seq)
	}
}

func TestCursorAtomicWriteAndCorruptRead(t *testing.T) {
	dir := t.TempDir()
	p := cursorPath(dir, "s", "r")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := writeCursor(p, 123); err != nil {
		t.Fatal(err)
	}
	if got := readCursor(p); got != 123 {
		t.Fatalf("got %d", got)
	}
	_ = os.WriteFile(p, []byte("garbage"), 0o600)
	if got := readCursor(p); got != 0 {
		t.Fatalf("corrupt cursor must read as 0, got %d", got)
	}
	if got := readCursor(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing cursor must read as 0, got %d", got)
	}
}

// TestReadFromRespectsMaxLimit は readFrom が max 件ちょうどで止まることを
// 確認する (`for len(out) < max` が `<=` になると max+1 件読んでしまう)。
func TestReadFromRespectsMaxLimit(t *testing.T) {
	dir := t.TempDir()
	w, _ := openWAL(dir, "s", "r")
	for i := 0; i < 5; i++ {
		_, _ = w.Append(newTestEvent(KindUsage))
	}
	recs, err := readFrom(walPath(dir, "s", "r"), 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("recs=%d, want exactly max=3", len(recs))
	}
}

// TestReadFromCleanEOFDoesNotWarn は改行で終わる完全な WAL を末尾まで読んだとき
// 「dropping partial tail line」を誤って警告しないことを確認する
// (`if len(line) > 0` が壊れると、行の無い正常な EOF でも警告してしまう)。
func TestReadFromCleanEOFDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	w, _ := openWAL(dir, "s", "r")
	_, _ = w.Append(newTestEvent(KindUsage))

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	recs, err := readFrom(walPath(dir, "s", "r"), 0, 100)
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	if bytes.Contains(buf.Bytes(), []byte("dropping partial tail line")) {
		t.Fatalf("clean EOF must not warn about a partial tail: %s", buf.String())
	}
}

func TestRunLockExcludesOthers(t *testing.T) {
	dir := t.TempDir()
	f, err := acquireRunLock(dir, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := tryLockRun(dir, "r1"); ok {
		t.Fatal("live run must not be lockable")
	}
	if g, ok := tryLockRun(dir, "r2"); !ok {
		t.Fatal("dead run must be lockable")
	} else {
		g.Close()
	}
}
