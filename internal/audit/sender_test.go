package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type fakeIggy struct {
	srv        *httptest.Server
	logins     atomic.Int32
	failSends  atomic.Int32 // 残りの 5xx 回数
	received   [][]byte
	topics     map[string]bool
	streamMade bool
}

func newFakeIggy(t *testing.T) *fakeIggy {
	f := &fakeIggy{topics: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, r *http.Request) {
		f.logins.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": map[string]any{"token": "tok"}})
	})
	mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) {
		if !f.streamMade {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /streams", func(w http.ResponseWriter, r *http.Request) { f.streamMade = true; w.WriteHeader(201) })
	mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) {
		if !f.topics[r.PathValue("t")] {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /streams/{s}/topics", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if f.topics[b.Name] {
			w.WriteHeader(409)
			return
		}
		f.topics[b.Name] = true
		w.WriteHeader(201)
	})
	mux.HandleFunc("POST /streams/{s}/topics/{t}/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if f.failSends.Load() > 0 {
			f.failSends.Add(-1)
			w.WriteHeader(500)
			return
		}
		var b struct {
			Messages []struct {
				Payload string `json:"payload"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		for _, m := range b.Messages {
			raw, _ := base64.StdEncoding.DecodeString(m.Payload)
			f.received = append(f.received, raw)
		}
		w.WriteHeader(201)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestSenderSendsFromCursorAndAdvances(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	w, _ := openWAL(dir, "s1", "r1")
	for i := 0; i < 3; i++ {
		e := newTestEvent(KindUsage)
		e.SessionID, e.RunID = "s1", "r1"
		e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":1}`)
		_, _ = w.Append(e)
	}
	c := newIggyClient(fi.srv.URL, "pat", "agent-audit", "0")
	s := &sender{dir: dir, runID: "r1", client: c}
	if err := s.drainRun(context.Background(), []string{"s1"}, "r1"); err != nil {
		t.Fatal(err)
	}
	if len(fi.received) != 3 {
		t.Fatalf("received=%d", len(fi.received))
	}
	if fi.logins.Load() != 1 {
		t.Fatalf("logins=%d", fi.logins.Load())
	}
	info, _ := os.Stat(walPath(dir, "s1", "r1"))
	if readCursor(cursorPath(dir, "s1", "r1")) != info.Size() {
		t.Fatal("cursor must reach end of wal")
	}
	// 2 回目は何も送らない
	if err := s.drainRun(context.Background(), []string{"s1"}, "r1"); err != nil {
		t.Fatal(err)
	}
	if len(fi.received) != 3 {
		t.Fatalf("resent: received=%d", len(fi.received))
	}
}

func TestSenderRetriesAfter5xxAndReloginsOn401(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	fi.failSends.Store(1)
	w, _ := openWAL(dir, "s1", "r1")
	e := newTestEvent(KindUsage)
	e.SessionID, e.RunID = "s1", "r1"
	e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":1}`)
	_, _ = w.Append(e)
	c := newIggyClient(fi.srv.URL, "pat", "agent-audit", "0")
	c.token = "stale" // 401 を誘発
	s := &sender{dir: dir, runID: "r1", client: c}
	if err := s.drainRun(context.Background(), []string{"s1"}, "r1"); err == nil {
		t.Fatal("first drain must fail on 500")
	}
	if err := s.drainRun(context.Background(), []string{"s1"}, "r1"); err != nil {
		t.Fatal(err)
	}
	if len(fi.received) != 1 || fi.logins.Load() < 1 {
		t.Fatalf("received=%d logins=%d", len(fi.received), fi.logins.Load())
	}
}

// TestSenderBackoffIgnoresWakeWhileFailing は送信が 500 で失敗し続けている間、
// emit のたびに飛ぶ wake が backoff の待ち時間を短縮しないことを確認する。
// 修正前は wake が backoff 中も有効なため、イベント数にほぼ比例して送信試行が
// 増える (リトライ間隔が実質縮む)
func TestSenderBackoffIgnoresWakeWhileFailing(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	fi.failSends.Store(100000) // テスト時間内は常に 500 を返す
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	ctx := WithSessionID(context.Background(), "s")

	deadline := time.Now().Add(1500 * time.Millisecond)
	for i := 0; i < 50; i++ {
		e.Usage(ctx, "p", "m", llm.Usage{InputTokens: 1})
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(1500 * time.Millisecond / 50)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	attempts := 100000 - fi.failSends.Load()
	if attempts > 6 {
		t.Fatalf("send attempts=%d, want <=6 (backoff must not shorten under wake pressure)", attempts)
	}
}

func TestScanDeadRunsSendsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	// 死んだ run r0 の WAL（lock は保持されていない）
	w, _ := openWAL(dir, "s1", "r0")
	e := newTestEvent(KindUsage)
	e.SessionID, e.RunID = "s1", "r0"
	e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":1}`)
	_, _ = w.Append(e)
	_ = os.WriteFile(lockPath(dir, "r0"), nil, 0o600)
	// 生きている run r1
	live, _ := acquireRunLock(dir, "r1")
	defer live.Close()
	w1, _ := openWAL(dir, "s1", "r1")
	_, _ = w1.Append(e)
	c := newIggyClient(fi.srv.URL, "pat", "agent-audit", "0")
	s := &sender{dir: dir, runID: "r2", client: c}
	s.scanDeadRuns(context.Background())
	if len(fi.received) != 1 {
		t.Fatalf("received=%d", len(fi.received))
	}
	if _, err := os.Stat(walPath(dir, "s1", "r0")); !os.IsNotExist(err) {
		t.Fatal("dead run wal must be deleted")
	}
	if _, err := os.Stat(lockPath(dir, "r0")); !os.IsNotExist(err) {
		t.Fatal("dead run lock must be deleted")
	}
	if _, err := os.Stat(walPath(dir, "s1", "r1")); err != nil {
		t.Fatal("live run wal must be kept")
	}
}

func TestEncodePayloadTruncatesOver64MB(t *testing.T) {
	e := newTestEvent(KindLLMRequest)
	e.Payload = json.RawMessage(`{"messages":[{"role":"user","content":"` + strings.Repeat("a", MaxPayloadBytes+10) + `"}]}`)
	line, _ := e.Marshal()
	enc, truncated := encodePayloadForIggy(line)
	if !truncated {
		t.Fatal("must truncate")
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	var got Event
	_ = json.Unmarshal(raw, &got)
	var tp TruncatedPayload
	if err := json.Unmarshal(got.Payload, &tp); err != nil || !tp.Truncated || tp.Bytes <= MaxPayloadBytes {
		t.Fatalf("payload=%s", got.Payload)
	}
}
