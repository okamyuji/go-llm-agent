// Package main 監査イベント (audit) が実 agent バイナリを通した主要導線
// (run / serve ヘッダあり・なし / Iggy 不達 / IGGY_PAT 未設定) で欠落しないことを
// 検証するフィクスチャ。tests/e2e/29-audit.sh から実行される。
// stub LLM は tests/e2e/fixtures/hooks_exercise・compaction_exercise と同じ形の
// httptest SSE サーバ、stub Iggy は internal/audit/sender_test.go の fakeIggy と
// 同じ振る舞い（ただし /messages の最初の N 回だけ 500 を返せる）で立てる。
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ---- stub Iggy ----------------------------------------------------------

// recvEvent stub Iggy が受け取った 1 監査イベントから取り出す最小情報
type recvEvent struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
	CallID    string `json:"call_id,omitempty"`
	Seq       uint64 `json:"seq"`
}

// stubIggy internal/audit/sender_test.go の fakeIggy と同じ REST 契約を持つ stub。
// failSends 回だけ /messages を 500 で失敗させ、以降は成功させる
type stubIggy struct {
	srv        *httptest.Server
	failSends  atomic.Int32
	mu         sync.Mutex
	received   []recvEvent
	streamMade bool
	topics     map[string]bool
}

func newStubIggy(failSends int32) *stubIggy {
	s := &stubIggy{topics: map[string]bool{}}
	s.failSends.Store(failSends)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /personal-access-tokens/login", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": map[string]any{"token": "tok"}})
	})
	mux.HandleFunc("GET /streams/{s}", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		made := s.streamMade
		s.mu.Unlock()
		if !made {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /streams", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.streamMade = true
		s.mu.Unlock()
		w.WriteHeader(201)
	})
	mux.HandleFunc("GET /streams/{s}/topics/{t}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		ok := s.topics[r.PathValue("t")]
		s.mu.Unlock()
		if !ok {
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
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.topics[b.Name] {
			w.WriteHeader(409)
			return
		}
		s.topics[b.Name] = true
		w.WriteHeader(201)
	})
	mux.HandleFunc("POST /streams/{s}/topics/{t}/messages", func(w http.ResponseWriter, r *http.Request) {
		if s.failSends.Add(-1) >= 0 {
			w.WriteHeader(500)
			return
		}
		s.failSends.Add(1) // 使い切った後は減らさない
		var b struct {
			Messages []struct {
				Payload string `json:"payload"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.mu.Lock()
		for _, m := range b.Messages {
			raw, err := base64.StdEncoding.DecodeString(m.Payload)
			if err != nil {
				continue
			}
			var ev recvEvent
			if json.Unmarshal(raw, &ev) == nil {
				s.received = append(s.received, ev)
			}
		}
		s.mu.Unlock()
		w.WriteHeader(201)
	})
	s.srv = httptest.NewServer(mux)
	return s
}

func (s *stubIggy) Close() { s.srv.Close() }

func (s *stubIggy) snapshot() []recvEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recvEvent, len(s.received))
	copy(out, s.received)
	return out
}

func (s *stubIggy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

// waitForCount count 件届くまで最大 timeout 待つ。stub Iggy 自身の 500 -> 成功への
// 復旧はエージェント側の送信 goroutine のバックオフ (最大 30 秒周期) に任せる
func (s *stubIggy) waitForCount(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.count() >= n {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return s.count() >= n
}

// ---- stub LLM (OpenAI 互換 SSE) -----------------------------------------

// newToolCallStub 1 ホップ目に shell ツール呼出、2 ホップ目に最終回答を返す
func newToolCallStub() *httptest.Server {
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		first := calls.Add(1) == 1
		w.Header().Set("Content-Type", "text/event-stream")
		var payload string
		if first {
			payload = `{"choices":[{"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":{"command":"echo","args":["hi"]}}}]},"finish_reason":"tool_calls"}]}`
		} else {
			payload = `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// newPlainStub ツール呼出をせず、毎回 reply を最終回答として返す
func newPlainStub(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := fmt.Sprintf(`{"choices":[{"delta":{"content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, reply)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// ---- config / binary helpers ---------------------------------------------

type configOpts struct {
	dir        string
	llmURL     string
	iggyURL    string
	serverAddr string
	tools      bool // shell を enabled_tools に含めるか
}

func writeConfig(o configOpts) (string, error) {
	enabledTools := "[]"
	if o.tools {
		enabledTools = "\n    - shell"
	}
	cfg := "default_model: openai/stub\n" +
		"providers:\n" +
		"  openai:\n" +
		"    base_url: " + o.llmURL + "/v1\n" +
		"    api_key_env: AUDIT_E2E_STUB_KEY\n" +
		"    allow_models:\n" +
		"      - stub\n" +
		"agent:\n" +
		"  max_tool_hops: 2\n" +
		"  enabled_tools: " + enabledTools + "\n" +
		"  compaction:\n" +
		"    enabled: false\n" +
		"  agents_md:\n" +
		"    enabled: false\n" +
		"storage:\n" +
		"  sessions_dir: " + o.dir + "/sessions\n" +
		"  chat_sessions_dir: " + o.dir + "/chat\n" +
		"tools:\n" +
		"  fs:\n" +
		"    allow_paths:\n" +
		"      - " + o.dir + "\n" +
		"  shell:\n" +
		"    allow_binaries:\n" +
		"      - echo\n" +
		"audit:\n" +
		"  iggy_url: " + o.iggyURL + "\n" +
		"  wal_dir: " + o.dir + "/audit-wal\n" +
		"  stream: agent-audit\n"
	if o.serverAddr != "" {
		cfg += "server:\n  addr: " + o.serverAddr + "\n"
	}
	path := filepath.Join(o.dir, "config.yaml")
	return path, os.WriteFile(path, []byte(cfg), 0o600)
}

// buildAgent agent バイナリを 1 回だけビルドして絶対パスを返す
func buildAgent(ctx context.Context, dir string) (string, error) {
	bin := filepath.Join(dir, "agent")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/okamyuji/go-llm-agent/cmd/agent")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent のビルド: %w", err)
	}
	return bin, nil
}

// freeAddr 未使用の 127.0.0.1 ポートを確保してアドレス文字列を返す
func freeAddr(ctx context.Context) (string, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

// waitDial addr が accept を始めるまで待つ
func waitDial(ctx context.Context, addr string, timeout time.Duration) bool {
	var d net.Dialer
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dialCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		c, err := d.DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---- flows ----------------------------------------------------------------

// result 1 導線の判定結果
type result struct {
	name string
	err  error
}

func (r result) print() {
	if r.err != nil {
		fmt.Printf("FAIL %s: %v\n", r.name, r.err)
		return
	}
	fmt.Printf("PASS %s\n", r.name)
}

// expectedKindOrder run 導線で必須のイベント種別の並び (usage は任意なので含めない)
var expectedKindOrder = []string{"llm_request", "llm_response", "tool_call", "tool_result", "llm_request", "llm_response"}

// flowRun agent run が 1 回のツール呼出を含む会話を実行し、監査イベント一式が
// 欠落なく・順序通りに stub Iggy へ届くことを確認する
func flowRun(ctx context.Context, bin string) result {
	name := "run"
	dir, err := os.MkdirTemp("", "audit-run-*")
	if err != nil {
		return result{name, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	llm := newToolCallStub()
	defer llm.Close()
	iggy := newStubIggy(0) // このフローは復旧経路ではなく順序を見るので即成功させる
	defer iggy.Close()

	cfgPath, err := writeConfig(configOpts{dir: dir, llmURL: llm.URL, iggyURL: iggy.srv.URL, tools: true})
	if err != nil {
		return result{name, err}
	}

	cmd := exec.CommandContext(ctx, bin, "run", "-config", cfgPath, "-p", "実行して") //nolint:gosec // fixture が組み立てた一時パス
	cmd.Env = append(os.Environ(), "IGGY_PAT=test", "AUDIT_E2E_STUB_KEY=stub")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return result{name, fmt.Errorf("agent run 失敗: %w (stderr=%s)", err, errBuf.String())}
	}
	if !strings.Contains(out.String(), "done") {
		return result{name, fmt.Errorf("最終回答が出力に無い: %q", out.String())}
	}

	got := iggy.snapshot()
	if len(got) < len(expectedKindOrder) {
		return result{name, fmt.Errorf("受信イベント数不足: got=%d want>=%d (%+v)", len(got), len(expectedKindOrder), got)}
	}
	// usage (任意) を除いた必須列だけを取り出して順序を照合する
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	if !containsInOrder(kinds, expectedKindOrder) {
		return result{name, fmt.Errorf("イベント順序が不正: got=%v want (部分列として)=%v", kinds, expectedKindOrder)}
	}
	// seq が 0 起点で受信順に連番であること (この導線は 1 session/1 run しか使わない)
	for i, e := range got {
		if e.Seq != uint64(i) {
			return result{name, fmt.Errorf("seq が連番でない: index=%d seq=%d (got=%+v)", i, e.Seq, got)}
		}
	}
	// tool_call / tool_result / (tool_call を伴う) llm_response が call_id を共有すること
	var toolCallID, toolResultID, toolLLMResponseID string
	for _, e := range got {
		switch e.Kind {
		case "tool_call":
			toolCallID = e.CallID
		case "tool_result":
			toolResultID = e.CallID
		case "llm_response":
			if e.CallID != "" {
				toolLLMResponseID = e.CallID
			}
		}
	}
	if toolCallID == "" || toolCallID != toolResultID {
		return result{name, fmt.Errorf("tool_call/tool_result の call_id が一致しない: call=%q result=%q", toolCallID, toolResultID)}
	}
	if toolLLMResponseID != toolCallID {
		return result{name, fmt.Errorf("ツール呼出を伴う llm_response の call_id が tool_call と一致しない: llm_response=%q tool_call=%q", toolLLMResponseID, toolCallID)}
	}
	// session_id が全イベントで同一・非空であること。run サブコマンドは Input.SessionID を
	// 設定しないため、実装上は audit.NormalizeSessionID のフォールバック "run-<runID>" になる
	sid := got[0].SessionID
	if sid == "" {
		return result{name, fmt.Errorf("session_id が空")}
	}
	for _, e := range got {
		if e.SessionID != sid {
			return result{name, fmt.Errorf("session_id が揃っていない: %q vs %q", sid, e.SessionID)}
		}
	}
	if !strings.HasPrefix(sid, "run-") {
		return result{name, fmt.Errorf("run 導線の session_id が run- フォールバックでない (実装変更の可能性): %q", sid)}
	}
	return result{name, nil}
}

// containsInOrder want が got の部分列 (順序を保った) として現れるか
func containsInOrder(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// flowServe agent serve を長生きさせ、(1) X-Session-Id ヘッダの伝播、(2) ヘッダ無し/不正
// ヘッダ時の UUID フォールバック、(3) stub Iggy が最初の 3 回 500 を返しても
// 最終的に全イベントが届くこと、を 1 プロセスの寿命の中でまとめて確認する
func flowServe(ctx context.Context, bin string) result {
	name := "serve"
	dir, err := os.MkdirTemp("", "audit-serve-*")
	if err != nil {
		return result{name, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	llm := newPlainStub("ok")
	defer llm.Close()
	iggy := newStubIggy(3) // 最初の 3 回の /messages を 500 にする
	defer iggy.Close()

	addr, err := freeAddr(ctx)
	if err != nil {
		return result{name, err}
	}
	cfgPath, err := writeConfig(configOpts{dir: dir, llmURL: llm.URL, iggyURL: iggy.srv.URL, serverAddr: addr})
	if err != nil {
		return result{name, err}
	}

	cmd := exec.CommandContext(ctx, bin, "serve", "-config", cfgPath) //nolint:gosec // fixture が組み立てた一時パス
	cmd.Env = append(os.Environ(), "IGGY_PAT=test", "AUDIT_E2E_STUB_KEY=stub")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return result{name, fmt.Errorf("agent serve 起動失敗: %w", err)}
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}()
	if !waitDial(ctx, addr, 5*time.Second) {
		return result{name, fmt.Errorf("serve が起動しない (stderr=%s)", errBuf.String())}
	}

	post := func(header string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/chat/completions",
			bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)))
		if err != nil {
			return "", err
		}
		if header != "" {
			req.Header.Set("X-Session-Id", header)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			return "", fmt.Errorf("status=%d body=%s", res.StatusCode, string(b))
		}
		return string(b), nil
	}

	if _, err := post("e2e-abc"); err != nil {
		return result{name, fmt.Errorf("header付きリクエスト失敗: %w", err)}
	}
	if _, err := post(""); err != nil {
		return result{name, fmt.Errorf("header無しリクエスト失敗: %w", err)}
	}
	if _, err := post("../evil"); err != nil {
		return result{name, fmt.Errorf("不正headerリクエスト失敗: %w", err)}
	}

	// 3 リクエスト分 (llm_request/llm_response/usage) x 3 = 9 イベント。
	// stub Iggy が最初の 3 回失敗しても、送信 goroutine のバックオフで
	// プロセスが生きている間に復旧して届くことを待って確認する
	if !iggy.waitForCount(9, 20*time.Second) {
		return result{name, fmt.Errorf("iggy 不達からの復旧: 20 秒待っても揃わない (got=%d want=9)", iggy.count())}
	}

	got := iggy.snapshot()
	var headeredCount int
	// 送信順は保証されないため、session_id の値で仕分けする
	seen := map[string]bool{}
	var uuids, unexpected []string
	for _, e := range got {
		switch {
		case e.SessionID == "e2e-abc":
			headeredCount++
		case len(e.SessionID) == 36:
			uuids = append(uuids, e.SessionID)
			seen[e.SessionID] = true
		default:
			unexpected = append(unexpected, e.SessionID)
		}
	}
	if headeredCount == 0 {
		return result{name, fmt.Errorf("X-Session-Id ヘッダが伝播していない")}
	}
	if len(unexpected) > 0 {
		return result{name, fmt.Errorf("ヘッダ値でも UUID でもない session_id が混入した（不正ヘッダの素通し）: %v", unexpected)}
	}
	if len(seen) < 2 {
		return result{name, fmt.Errorf("ヘッダ無し/不正ヘッダの session_id が UUID として 2 種類に分かれていない: %v", uuids)}
	}
	return result{name, nil}
}

// flowIggyDown Iggy に完全に到達できない状態でも run が正常終了し最終回答を出すことを確認する
func flowIggyDown(ctx context.Context, bin string) result {
	name := "iggy_down"
	dir, err := os.MkdirTemp("", "audit-down-*")
	if err != nil {
		return result{name, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	llm := newPlainStub("still-ok")
	defer llm.Close()

	// listen だけして即 close することで、接続が確実に拒否されるポートを用意する
	deadAddr, err := freeAddr(ctx)
	if err != nil {
		return result{name, err}
	}

	cfgPath, err := writeConfig(configOpts{dir: dir, llmURL: llm.URL, iggyURL: "http://" + deadAddr})
	if err != nil {
		return result{name, err}
	}

	cmd := exec.CommandContext(ctx, bin, "run", "-config", cfgPath, "-p", "hi") //nolint:gosec // fixture が組み立てた一時パス
	cmd.Env = append(os.Environ(), "IGGY_PAT=test", "AUDIT_E2E_STUB_KEY=stub")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return result{name, fmt.Errorf("iggy 不通でも exit 0 のはずが失敗: %w (stderr=%s)", err, errBuf.String())}
	}
	if !strings.Contains(out.String(), "still-ok") {
		return result{name, fmt.Errorf("iggy 不通時に最終回答が出力されない: %q", out.String())}
	}
	return result{name, nil}
}

// findDeadWAL wal_dir 直下を 1 段だけ探索し、最初に見つかった session/run の WAL の
// 行数・session_id・run_id を返す (flowIggyRecovery は同一 wal_dir に 1 run しか
// 書かない前提)
func findDeadWAL(walDir string) (sessionID, runID string, lines int, err error) {
	sessDirs, err := os.ReadDir(walDir)
	if err != nil {
		return "", "", 0, err
	}
	for _, sd := range sessDirs {
		if !sd.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(walDir, sd.Name()))
		if err != nil {
			return "", "", 0, err
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(walDir, sd.Name(), f.Name()))
			if err != nil {
				return "", "", 0, err
			}
			n := len(strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
			return sd.Name(), strings.TrimSuffix(f.Name(), ".jsonl"), n, nil
		}
	}
	return "", "", 0, fmt.Errorf("wal_dir=%s に .jsonl が見つからない", walDir)
}

// flowIggyRecovery Iggy に一切到達できない状態で 1 run 分の WAL をディスクに残し、
// 別プロセスの agent run が起動時の scanDeadRuns でその「死んだ run」を発見して
// 新しい実行と一緒に完全にドレインする (WAL/cursor/lock ファイルが消える) ことを確認する。
// これが監査の「欠落なし」の本丸: 送信できなかったイベントが後続の起動で必ず回収される
func flowIggyRecovery(ctx context.Context, bin string) result {
	name := "iggy_recovery"
	dir, err := os.MkdirTemp("", "audit-recovery-*")
	if err != nil {
		return result{name, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// 1st run: Iggy 到達不能。llm はツール呼出を挟み 6 イベント (>=5) を WAL に残す
	llm1 := newToolCallStub()
	defer llm1.Close()
	deadAddr, err := freeAddr(ctx)
	if err != nil {
		return result{name, err}
	}
	cfg1, err := writeConfig(configOpts{dir: dir, llmURL: llm1.URL, iggyURL: "http://" + deadAddr, tools: true})
	if err != nil {
		return result{name, err}
	}
	cmd1 := exec.CommandContext(ctx, bin, "run", "-config", cfg1, "-p", "実行して") //nolint:gosec // fixture が組み立てた一時パス
	cmd1.Env = append(os.Environ(), "IGGY_PAT=test", "AUDIT_E2E_STUB_KEY=stub")
	var out1, errBuf1 bytes.Buffer
	cmd1.Stdout, cmd1.Stderr = &out1, &errBuf1
	if err := cmd1.Run(); err != nil {
		return result{name, fmt.Errorf("1 回目の run 失敗: %w (stderr=%s)", err, errBuf1.String())}
	}
	if !strings.Contains(out1.String(), "done") {
		return result{name, fmt.Errorf("1 回目の最終回答が出力に無い: %q", out1.String())}
	}

	walDir := filepath.Join(dir, "audit-wal")
	sid1, runID1, lines, err := findDeadWAL(walDir)
	if err != nil {
		return result{name, err}
	}
	if lines < 5 {
		return result{name, fmt.Errorf("1 回目の WAL 行数不足: got=%d want>=5", lines)}
	}

	// 2nd run: 同じ wal_dir、今度は生きている stub Iggy へ向ける。
	// 起動時の scanDeadRuns が 1 回目の run (プロセス終了でロックが外れている) を
	// 見つけてドレインし、自分自身のイベントと合わせて送信する
	llm2 := newPlainStub("second-answer")
	defer llm2.Close()
	iggy := newStubIggy(0)
	defer iggy.Close()
	cfg2, err := writeConfig(configOpts{dir: dir, llmURL: llm2.URL, iggyURL: iggy.srv.URL})
	if err != nil {
		return result{name, err}
	}
	cmd2 := exec.CommandContext(ctx, bin, "run", "-config", cfg2, "-p", "hi") //nolint:gosec // fixture が組み立てた一時パス
	cmd2.Env = append(os.Environ(), "IGGY_PAT=test", "AUDIT_E2E_STUB_KEY=stub")
	var out2, errBuf2 bytes.Buffer
	cmd2.Stdout, cmd2.Stderr = &out2, &errBuf2
	if err := cmd2.Run(); err != nil {
		return result{name, fmt.Errorf("2 回目の run 失敗: %w (stderr=%s)", err, errBuf2.String())}
	}

	if !iggy.waitForCount(lines, 20*time.Second) {
		return result{name, fmt.Errorf("死んだ run のドレインが揃わない: got=%d want>=%d", iggy.count(), lines)}
	}
	runIDs := map[string]bool{}
	for _, e := range iggy.snapshot() {
		runIDs[e.RunID] = true
	}
	if len(runIDs) < 2 {
		return result{name, fmt.Errorf("2 つの run_id が届いていない: %v", runIDs)}
	}
	if !runIDs[runID1] {
		return result{name, fmt.Errorf("1 回目の run_id %q が届いていない: %v", runID1, runIDs)}
	}

	for _, p := range []string{
		filepath.Join(walDir, sid1, runID1+".jsonl"),
		filepath.Join(walDir, sid1, runID1+".cursor"),
		filepath.Join(walDir, runID1+".lock"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			return result{name, fmt.Errorf("死んだ run のファイルが残っている: %s", p)}
		}
	}
	return result{name, nil}
}

// flowNoPAT IGGY_PAT 未設定時に監査が無効化され、警告が出て wal_dir が作られず
// stub Iggy に何も届かないことを確認する
func flowNoPAT(ctx context.Context, bin string) result {
	name := "iggy_pat_unset"
	dir, err := os.MkdirTemp("", "audit-nopat-*")
	if err != nil {
		return result{name, err}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	llm := newPlainStub("no-pat-ok")
	defer llm.Close()
	iggy := newStubIggy(0)
	defer iggy.Close()

	cfgPath, err := writeConfig(configOpts{dir: dir, llmURL: llm.URL, iggyURL: iggy.srv.URL})
	if err != nil {
		return result{name, err}
	}

	// IGGY_PAT を明示的に除いた環境を組み立てる
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "IGGY_PAT=") {
			env = append(env, kv)
		}
	}
	env = append(env, "AUDIT_E2E_STUB_KEY=stub")
	cmd := exec.CommandContext(ctx, bin, "run", "-config", cfgPath, "-p", "hi") //nolint:gosec // fixture が組み立てた一時パス
	cmd.Env = env
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return result{name, fmt.Errorf("IGGY_PAT 未設定でも exit 0 のはずが失敗: %w (stderr=%s)", err, errBuf.String())}
	}
	if !strings.Contains(errBuf.String(), "IGGY_PAT") {
		return result{name, fmt.Errorf("stderr に IGGY_PAT 警告が無い: %q", errBuf.String())}
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-wal")); err == nil {
		return result{name, fmt.Errorf("監査無効時に wal_dir が作られてしまった")}
	}
	time.Sleep(300 * time.Millisecond) // 万一の非同期送信が無いことを確認する猶予
	if n := iggy.count(); n != 0 {
		return result{name, fmt.Errorf("監査無効時に stub Iggy が %d 件受信した (want 0)", n)}
	}
	return result{name, nil}
}

func main() {
	// 子プロセスは全て exec.CommandContext(ctx) なので、この上限が agent の hang を止める最後の砦
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("", "audit-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(root) }()

	bin, err := buildAgent(ctx, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}

	results := []result{
		flowRun(ctx, bin),
		flowServe(ctx, bin),
		flowIggyDown(ctx, bin),
		flowIggyRecovery(ctx, bin),
		flowNoPAT(ctx, bin),
	}
	// chat の /compact 経由の監査は creack/pty での対話駆動が必要で、tty_exercise 相当の
	// 追加実装コストに見合う導線差分 (llm_request/llm_response が Chat 経由でも同じ
	// session_id で届くことは serve 導線で既に検証済みの Emitter 経路を通るだけ) が
	// 小さいため、このフィクスチャでは意図的に SKIP する
	fmt.Println("SKIP chat_compact: pty 経由の対話駆動が必要なため未実装 (run/serve で Emitter 経路は検証済み)")

	failed := false
	for _, r := range results {
		r.print()
		if r.err != nil {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
