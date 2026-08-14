// Package main 11-japanese-ux.md 5.2 節の日本語 TTY 検証フィクスチャ (D-16)。
// creack/pty で擬似端末を割り当てて agent バイナリを起動し、CJK 入力の編集・
// カーソル移動・Backspace・Ctrl-R 検索・幅描画のエスケープ列を検証する。
// stub LLM は D-17 に従い fixture 内の httptest サーバで立てる。
//
// 設計書からの逸脱: 検証項目 5 (折返し) だけ winsize 20 桁の子プロセスを別に起動する。
// 行エディタは起動時に一度だけ winsize を読み SIGWINCH を購読しないため、
// 1 プロセス内での桁数切替ができない。また 20 桁では Ctrl-R の検索プロンプト
// "(reverse-i-search)”: " 自体が折り返され、検証項目 6-10 の表明が書けない。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// waitTimeout 1 つのエスケープ列が出るまで待つ上限。
// 変数にするのは、テストが短い値へ差し替えてタイムアウト経路を現実的な
// 実行時間で踏めるようにするため。実行時に書き換える経路は無い
var waitTimeout = 15 * time.Second

const (
	// historyEntry Ctrl-R 検索で最初に一致する履歴
	historyEntry = "日本語のテスト"
	// olderHistoryEntry Ctrl-R 再送でより過去へ遡ったときに一致する履歴
	olderHistoryEntry = "最初の日本語のテスト"
	// searchPrompt 検索開始直後の表示 (クエリ空)
	searchPrompt = "(reverse-i-search)'': "
	// wideCols 折返しを起こさない桁数
	wideCols = 80
	// narrowCols 検証項目 5 で折返しを起こす桁数
	narrowCols = 20
)

// キー入力のバイト列
const (
	keyLeft      = "\x1b[D"
	keyBackspace = "\x7f"
	keyCtrlU     = "\x15"
	keyCtrlK     = "\x0b"
	keyCtrlR     = "\x12"
	keyCtrlG     = "\x07"
	keyEnter     = "\r"
)

// stub OpenAI 互換 SSE スタブ。受け取った user メッセージを記録する
type stub struct {
	srv *httptest.Server

	mu       sync.Mutex
	userMsgs []string
}

func newStub() *stub {
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *stub) Close() { s.srv.Close() }

func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
		s.mu.Lock()
		for _, m := range p.Messages {
			if m.Role == "user" {
				s.userMsgs = append(s.userMsgs, m.Content)
			}
		}
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\n",
		`{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (s *stub) lastUserMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.userMsgs) == 0 {
		return ""
	}
	return s.userMsgs[len(s.userMsgs)-1]
}

// safeBuf pty から読み出したバイト列を保持する
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (x *safeBuf) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.b.Write(p)
}

func (x *safeBuf) String() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.b.String()
}

// session 擬似端末で動く agent 子プロセス 1 つ
type session struct {
	cmd *exec.Cmd
	f   *os.File
	out *safeBuf
	// cursor 表明済みの位置。waitFor はこれ以降だけを探す
	cursor int
}

// errNoPTY 擬似端末を割り当てられなかったことを示す
type errNoPTY struct{ err error }

func (e errNoPTY) Error() string { return "pty を割り当てられません: " + e.err.Error() }

// startSession 擬似端末を割り当てて agent chat を起動する
func startSession(dir, bin string, cols int) (*session, error) {
	cmd := exec.Command(bin, "chat", "-config", filepath.Join(dir, "config.yaml"), "-no-spinner") // #nosec G204 -- fixture が組み立てた一時パス
	cmd.Env = append(os.Environ(), "HOME="+dir, "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: uint16(cols)}) // #nosec G115 -- cols は定数
	if err != nil {
		return nil, errNoPTY{err: err}
	}
	s := &session{cmd: cmd, f: f, out: &safeBuf{}}
	go func() { _, _ = io.Copy(s.out, f) }()
	return s, nil
}

// close 子プロセスを確実に終了させる
func (s *session) close() {
	_ = s.f.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

func (s *session) send(b string) error {
	_, err := io.WriteString(s.f, b)
	return err
}

// waitFor cursor 以降の出力に want が現れるまで待ち、cursor を一致直後へ進める。
// 返り値は cursor 以降・一致直後までの出力 (表明の追加検査に使う)
func (s *session) waitFor(want string) (string, error) {
	deadline := time.Now().Add(waitTimeout)
	for {
		rest := s.out.String()[s.cursor:]
		if i := strings.Index(rest, want); i >= 0 {
			s.cursor += i + len(want)
			return rest[:i+len(want)], nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s が出力されない。直近の出力: %s", strconv.Quote(want), strconv.Quote(tail(rest, 200)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sendAndWait 入力を送り、期待するエスケープ列が出るまで待つ
func (s *session) sendAndWait(in, want string) (string, error) {
	if err := s.send(in); err != nil {
		return "", err
	}
	return s.waitFor(want)
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// writeConfig stub の URL を埋め込んだ一時 config.yaml を書き出す
func writeConfig(dir, baseURL string) error {
	cfg := "default_model: llamacpp/stub\n" +
		"providers:\n" +
		"  llamacpp:\n" +
		"    base_url: " + baseURL + "/v1\n" +
		"    allow_models:\n" +
		"      - stub\n" +
		"agent:\n" +
		"  max_tool_hops: 1\n" +
		"  enabled_tools: []\n" +
		"  compaction:\n" +
		"    enabled: false\n" +
		"  agents_md:\n" +
		"    enabled: false\n" +
		"storage:\n" +
		"  sessions_dir: " + dir + "/sessions\n" +
		"  chat_sessions_dir: " + dir + "/chat\n" +
		"tools:\n" +
		"  fs:\n" +
		"    allow_paths:\n" +
		"      - " + dir + "\n"
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600)
}

// writeHistory 検証項目 7・8 用の履歴を配置する。
// 利用者の実 ~/.agent_history には触れない (子プロセスの HOME を dir にする)
func writeHistory(dir string) error {
	body := olderHistoryEntry + "\n関係ない行\n" + historyEntry + "\n"
	return os.WriteFile(filepath.Join(dir, ".agent_history"), []byte(body), 0o600)
}

// buildAgent agent バイナリをビルドして絶対パスを返す
func buildAgent(ctx context.Context, dir string) (string, error) {
	bin := filepath.Join(dir, "agent")
	// パッケージパスで指定するのは、カレントディレクトリがリポジトリルートでない
	// 場合 (go test はパッケージディレクトリで走る) でも解決できるようにするため
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/okamyuji/go-llm-agent/cmd/agent")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent のビルド: %w", err)
	}
	return bin, nil
}

// checkStartup 検証項目 1・2
func checkStartup(s *session, out io.Writer) error {
	if _, err := s.waitFor("\x1b[?2004h"); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_bracketed_paste_on=true")
	if _, err := s.waitFor(">> "); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_prompt_shown=true")
	return nil
}

// checkEditing 検証項目 3・4
func checkEditing(s *session, out io.Writer) error {
	if _, err := s.sendAndWait("日本語", "日本語"); err != nil {
		return err
	}
	if _, err := s.sendAndWait(keyLeft, "\x1b[2D"); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_cursor_left_cjk=true")
	// Backspace は「左へ 2 セル移動 → 残りの行を再描画 → 消えた 2 セルを空白で潰す →
	// カーソルを 4 セル戻す」の順に出力する。最後の復帰まで待って空白 2 個を確認する
	got, err := s.sendAndWait(keyBackspace, "\x1b[4D")
	if err != nil {
		return err
	}
	if !strings.Contains(got, "語  ") {
		return fmt.Errorf("Backspace の消去に空白 2 個が含まれない: %s", strconv.Quote(got))
	}
	fmt.Fprintln(out, "tty_backspace_erases_cells=true")
	return nil
}

// checkSearch 検証項目 6・7・8・9
func checkSearch(s *session, out io.Writer) error {
	// 検索前の行を "abc" にする。Ctrl-G の復元が観測できるようにするため
	if err := s.send(keyCtrlU + keyCtrlK); err != nil {
		return err
	}
	if _, err := s.sendAndWait("abc", "abc"); err != nil {
		return err
	}
	if _, err := s.sendAndWait(keyCtrlR, searchPrompt); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_search_prompt=true")
	if _, err := s.sendAndWait("本語", "(reverse-i-search)'本語': "+historyEntry); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_search_candidate=true")
	if _, err := s.sendAndWait(keyCtrlR, "(reverse-i-search)'本語': "+olderHistoryEntry); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_search_older_candidate=true")
	if _, err := s.sendAndWait(keyCtrlG, ">> abc"); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_search_abort_restores=true")
	return nil
}

// checkSubmit 検証項目 10
func checkSubmit(s *session, st *stub, out io.Writer) error {
	if err := s.send(keyCtrlU + keyCtrlK); err != nil {
		return err
	}
	if _, err := s.sendAndWait(keyCtrlR+"本語", "(reverse-i-search)'本語': "+historyEntry); err != nil {
		return err
	}
	if _, err := s.sendAndWait(keyEnter, ">> "+historyEntry); err != nil {
		return err
	}
	if _, err := s.waitFor("OK"); err != nil {
		return err
	}
	if got := st.lastUserMessage(); got != historyEntry {
		return fmt.Errorf("stub が受け取った user メッセージ %q が候補 %q と一致しない", got, historyEntry)
	}
	fmt.Fprintln(out, "tty_search_submits_candidate=true")
	return nil
}

// checkQuit 検証項目 11
func checkQuit(s *session, out io.Writer) error {
	if _, err := s.sendAndWait("/quit"+keyEnter, "\x1b[?2004l"); err != nil {
		return err
	}
	fmt.Fprintln(out, "tty_bracketed_paste_off=true")
	return nil
}

// checkWrap 検証項目 5。プロンプト 3 セルに続けて CJK 9 文字を送ると、
// 9 文字目の前にパディング空白と改行が入り、折返し前の印字セル数が桁数を超えない
func checkWrap(dir, bin string, out io.Writer) error {
	s, err := startSession(dir, bin, narrowCols)
	if err != nil {
		return err
	}
	defer s.close()
	if _, err := s.waitFor(">> "); err != nil {
		return err
	}
	if _, err := s.sendAndWait("あいうえおかきく", "あいうえおかきく"); err != nil {
		return err
	}
	got, err := s.sendAndWait("け", "け")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(got, " \r\n") {
		return fmt.Errorf("9 文字目の前にパディング空白と改行が無い: %s", strconv.Quote(got))
	}
	// プロンプト 3 セル + CJK 8 文字 16 セル + パディング 1 セル = 桁数ちょうど
	if cells := 3 + 8*2 + 1; cells != narrowCols {
		return fmt.Errorf("折返し前の印字セル数 %d が %d と一致しない", cells, narrowCols)
	}
	fmt.Fprintln(out, "tty_wrap_pads_cjk=true")
	return nil
}

// runMainSession 折返し以外の検証項目を 1 つの子プロセスで順に確認する
func runMainSession(dir, bin string, st *stub, out io.Writer) error {
	s, err := startSession(dir, bin, wideCols)
	if err != nil {
		return err
	}
	defer s.close()
	if err := checkStartup(s, out); err != nil {
		return err
	}
	if err := checkEditing(s, out); err != nil {
		return err
	}
	if err := checkSearch(s, out); err != nil {
		return err
	}
	if err := checkSubmit(s, st, out); err != nil {
		return err
	}
	return checkQuit(s, out)
}

func run(ctx context.Context, dir string, out io.Writer) error {
	st := newStub()
	defer st.Close()
	if err := writeConfig(dir, st.srv.URL); err != nil {
		return err
	}
	if err := writeHistory(dir); err != nil {
		return err
	}
	bin, err := buildAgent(ctx, dir)
	if err != nil {
		return err
	}
	if err := runMainSession(dir, bin, st, out); err != nil {
		return err
	}
	return checkWrap(dir, bin, out)
}

// runInTempDir 一時ディレクトリを作って run を呼ぶ。
// os.Exit が defer を実行しないため、cleanup を伴う本体を main から分離する
func runInTempDir(ctx context.Context, out io.Writer) error {
	dir, err := os.MkdirTemp("", "tty-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	return run(ctx, dir, out)
}

// skipIfNoPTY 擬似端末を割り当てられなかった場合を skip (エラー無し) へ変換する
func skipIfNoPTY(err error, out io.Writer) error {
	var noPTY errNoPTY
	if err != nil && asNoPTY(err, &noPTY) {
		fmt.Fprintln(out, "tty_skipped=true")
		return nil
	}
	return err
}

func mainErr() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return skipIfNoPTY(runInTempDir(ctx, os.Stdout), os.Stdout)
}

// asNoPTY err が errNoPTY かを判定する
func asNoPTY(err error, target *errNoPTY) bool {
	e, ok := err.(errNoPTY) // nolint:errorlint // fixture 内で wrap しないため型アサーションで十分
	if ok {
		*target = e
	}
	return ok
}

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "tty_exercise:", err)
		os.Exit(1)
	}
}
