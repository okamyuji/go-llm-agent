package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewIggyClientHasThirtySecondTimeout は http.Client.Timeout が 30 秒である
// ことを直接検証する (`30 * time.Second` の演算子取り違えを検出する)。
func TestNewIggyClientHasThirtySecondTimeout(t *testing.T) {
	c := newIggyClient("http://example.invalid", "pat", "s", "0")
	if c.http.Timeout != 30*time.Second {
		t.Fatalf("timeout=%v, want 30s", c.http.Timeout)
	}
}

func loginOKMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": map[string]any{"token": "tok"}})
	})
	return mux
}

// TestEnsureStreamPaths は ensureStream の各分岐 (ネットワークエラーでの即
// return、既存ストリームでの作成スキップ、作成成功/競合/失敗) を検証する。
func TestEnsureStreamPaths(t *testing.T) {
	t.Run("network error short-circuits before create", func(t *testing.T) {
		c := newIggyClient("http://127.0.0.1:1", "pat", "s", "0")
		err := c.ensureStream(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "iggy create stream") {
			t.Fatalf("network error at the GET step must not reach the create-stream fallback: %v", err)
		}
	})

	t.Run("existing stream skips create", func(t *testing.T) {
		created := 0
		mux := loginOKMux()
		mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("POST /streams", func(w http.ResponseWriter, r *http.Request) { created++; w.WriteHeader(201) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := newIggyClient(srv.URL, "pat", "s", "0")
		if err := c.ensureStream(context.Background()); err != nil {
			t.Fatal(err)
		}
		if created != 0 {
			t.Fatalf("existing stream must not be recreated, create calls=%d", created)
		}
	})

	cases := []struct {
		name       string
		createCode int
		wantErr    bool
	}{
		{"create success", 201, false},
		{"create conflict is ok", http.StatusConflict, false},
		{"create failure propagates", 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := loginOKMux()
			mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
			mux.HandleFunc("POST /streams", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.createCode) })
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			c := newIggyClient(srv.URL, "pat", "s", "0")
			err := c.ensureStream(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestEnsureTopicPaths は ensureTopic の各分岐 (ネットワークエラー、既存トピックの
// 作成スキップ、作成成功/競合/失敗/body の already-exists 検知) を検証する。
func TestEnsureTopicPaths(t *testing.T) {
	t.Run("network error short-circuits before create", func(t *testing.T) {
		c := newIggyClient("http://127.0.0.1:1", "pat", "s", "0")
		err := c.ensureTopic(context.Background(), "t")
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "iggy create topic") {
			t.Fatalf("network error at the GET step must not reach the create-topic fallback: %v", err)
		}
	})

	t.Run("existing topic skips create", func(t *testing.T) {
		created := 0
		mux := loginOKMux()
		mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("POST /streams/{s}/topics", func(w http.ResponseWriter, r *http.Request) { created++; w.WriteHeader(201) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := newIggyClient(srv.URL, "pat", "s", "0")
		if err := c.ensureTopic(context.Background(), "t"); err != nil {
			t.Fatal(err)
		}
		if created != 0 {
			t.Fatalf("existing topic must not be recreated, create calls=%d", created)
		}
	})

	cases := []struct {
		name       string
		createCode int
		body       string
		wantErr    bool
	}{
		{"create success", 201, "", false},
		{"create conflict is ok", http.StatusConflict, "", false},
		{"already-exists body is ok despite non-2xx", 400, "topic already exists", false},
		{"create failure propagates", 500, "boom", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := loginOKMux()
			mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
			mux.HandleFunc("POST /streams/{s}/topics", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.createCode)
				_, _ = w.Write([]byte(tc.body))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			c := newIggyClient(srv.URL, "pat", "s", "0")
			err := c.ensureTopic(context.Background(), "t")
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestExpiryJSONValue は数値文字列を uint64 に復元することを検証する
// (`err == nil` の判定取り違えを検出する)。
func TestExpiryJSONValue(t *testing.T) {
	c := newIggyClient("http://example.invalid", "pat", "s", "7776000000000")
	if v := c.expiryJSONValue(); v != 7776000000000 {
		t.Fatalf("numeric expiry must decode to uint64, got %#v", v)
	}
}

// TestNewEmitterFallsBackToDefaultExpiryOnInvalidValue は Options.Expiry に
// 数値として解釈できない値が渡された場合、既定値 (90 日) にフォールバックする
// ことを検証する (invalid config value → default used)。
func TestNewEmitterFallsBackToDefaultExpiryOnInvalidValue(t *testing.T) {
	e := NewEmitter(Options{WALDir: t.TempDir(), Expiry: "not-a-number"})
	if e.opts.Expiry != defaultExpiry {
		t.Fatalf("invalid Expiry must fall back to default, got %q", e.opts.Expiry)
	}
}

// TestEncodePayloadBoundary は MaxPayloadBytes ちょうどでは切り詰めず、
// 1 バイト超えたら切り詰めることを検証する (`<=` の境界取り違えを検出する)。
func TestEncodePayloadBoundary(t *testing.T) {
	atLimit := make([]byte, MaxPayloadBytes)
	if _, truncated := encodePayloadForIggy(atLimit); truncated {
		t.Fatal("exactly MaxPayloadBytes must not be truncated")
	}
	overLimit := make([]byte, MaxPayloadBytes+1)
	if _, truncated := encodePayloadForIggy(overLimit); !truncated {
		t.Fatal("MaxPayloadBytes+1 must be truncated")
	}
}

// TestWithAuthShortCircuitsOnLoginFailure は token が空でログインに失敗した
// とき f を一度も呼ばないことを検証する (`err != nil` の判定取り違えを検出する)。
func TestWithAuthShortCircuitsOnLoginFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newIggyClient(srv.URL, "pat", "s", "0")
	s := &sender{dir: t.TempDir(), runID: "r1", client: c}
	calls := 0
	err := s.withAuth(context.Background(), func() error { calls++; return nil })
	if err == nil {
		t.Fatal("expected login failure to propagate")
	}
	if calls != 0 {
		t.Fatalf("f must not run when login fails, calls=%d", calls)
	}
}

// TestDrainRunPropagatesStageErrors は drainRun が ensureStream/ensureTopic/send
// のそれぞれの失敗を呼び出し元へ伝えることを検証する。
func TestDrainRunPropagatesStageErrors(t *testing.T) {
	setupWAL := func(t *testing.T, dir string) {
		t.Helper()
		w, err := openWAL(dir, "s1", "r1")
		if err != nil {
			t.Fatal(err)
		}
		e := newTestEvent(KindUsage)
		e.SessionID, e.RunID = "s1", "r1"
		e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":1}`)
		if _, err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("ensureStream error", func(t *testing.T) {
		dir := t.TempDir()
		setupWAL(t, dir)
		mux := loginOKMux()
		mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
		mux.HandleFunc("POST /streams", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := newIggyClient(srv.URL, "pat", "agent-audit", "0")
		s := &sender{dir: dir, runID: "r1", client: c}
		err := s.drainRun(context.Background(), []string{"s1"}, "r1")
		if err == nil || !strings.Contains(err.Error(), "create stream") {
			t.Fatalf("expected ensureStream failure to propagate, got %v", err)
		}
	})

	t.Run("ensureTopic error", func(t *testing.T) {
		dir := t.TempDir()
		setupWAL(t, dir)
		mux := loginOKMux()
		mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
		mux.HandleFunc("POST /streams/{s}/topics", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := newIggyClient(srv.URL, "pat", "agent-audit", "0")
		s := &sender{dir: dir, runID: "r1", client: c}
		err := s.drainRun(context.Background(), []string{"s1"}, "r1")
		if err == nil || !strings.Contains(err.Error(), "create topic") {
			t.Fatalf("expected ensureTopic failure to propagate, got %v", err)
		}
	})

	t.Run("send error", func(t *testing.T) {
		dir := t.TempDir()
		setupWAL(t, dir)
		mux := loginOKMux()
		mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("POST /streams/{s}/topics/{t}/messages", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := newIggyClient(srv.URL, "pat", "agent-audit", "0")
		s := &sender{dir: dir, runID: "r1", client: c}
		err := s.drainRun(context.Background(), []string{"s1"}, "r1")
		if err == nil || !strings.Contains(err.Error(), "iggy send") {
			t.Fatalf("expected send failure to propagate, got %v", err)
		}
	})
}

// TestDrainRunWriteCursorFailure は send 成功後に writeCursor が失敗した場合、
// drainRun がそのエラーを返すことを検証する
// (`if err := writeCursor(...); err != nil` の判定取り違えを検出する)。
func TestDrainRunWriteCursorFailure(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	w, err := openWAL(dir, "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEvent(KindUsage)
	e.SessionID, e.RunID = "s1", "r1"
	e.Payload = json.RawMessage(`{"input_tokens":1,"output_tokens":1}`)
	if _, err := w.Append(e); err != nil {
		t.Fatal(err)
	}
	sessDir := filepath.Join(dir, "s1")
	if err := os.Chmod(sessDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessDir, 0o700) })

	c := newIggyClient(fi.srv.URL, "pat", "agent-audit", "0")
	s := &sender{dir: dir, runID: "r1", client: c}
	err = s.drainRun(context.Background(), []string{"s1"}, "r1")
	if err == nil {
		t.Fatal("writeCursor failure must propagate")
	}
	if len(fi.received) != 1 {
		t.Fatalf("send must have succeeded before the cursor write failed, received=%d", len(fi.received))
	}
}

// TestSenderRunBackoffGrowsOnRepeatedFailure は run() のリトライ間隔が失敗の
// たびに倍増することを実時間で緩く検証する (backoff の doubling/cap 判定の
// 取り違えを検出する。3 回目までの間隔しか見ないため 30 秒キャップには到達しない)。
func TestSenderRunBackoffGrowsOnRepeatedFailure(t *testing.T) {
	var mu sync.Mutex
	var attempts []time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		w.WriteHeader(500)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newIggyClient(srv.URL, "pat", "s", "0")
	s := &sender{dir: t.TempDir(), runID: "r1", client: c, wake: make(chan struct{}, 1), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3200*time.Millisecond)
	defer cancel()
	s.run(ctx)

	mu.Lock()
	got := append([]time.Time{}, attempts...)
	mu.Unlock()
	if len(got) < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", len(got))
	}
	gap1 := got[1].Sub(got[0])
	gap2 := got[2].Sub(got[1])
	if gap1 < 600*time.Millisecond || gap1 > 1500*time.Millisecond {
		t.Fatalf("first retry gap=%v, want ~1s", gap1)
	}
	// gap2 が gap1 のほぼ倍でなければ backoff が成長していない
	// (negation/arithmetic 変異ではキャップ判定が常に偽になり間隔が 1s のまま固定される)
	if gap2 < gap1+400*time.Millisecond {
		t.Fatalf("backoff must roughly double: gap1=%v gap2=%v", gap1, gap2)
	}
}
