package audit

// Iggy 0.8.0 REST（確認元: https://raw.githubusercontent.com/apache/iggy/server-0.8.0/core/server/server.http）
// - POST /personal-access-tokens/login {"token": "..."} → access_token.token
// - POST /streams のボディは {"name": "..."} のみ。stream_id は不要（server.http 212-218 行）
// - POST /streams/{stream}/topics {"name","partitions_count","compression_algorithm":"none","max_topic_size":0,"message_expiry":N}（server.http 246-256 行）
// - message_expiry はマイクロ秒の u64（JSON 数値）。文字列は受け付けない
//   （core/common/src/utils/expiry.rs の IggyExpiry::deserialize は deserialize_u64 のみ実装、visit_u64 以外は未実装）。
//   0 = ServerDefault、u64::MAX = NeverExpire。90 日 = 90*24*60*60*1_000_000 = 7776000000000 マイクロ秒
// - GET /streams/{stream_id} と GET .../topics/{topic_id} の {stream_id}/{topic_id} は Identifier で、
//   数値としてパースできなければ名前として扱われる（core/common/src/types/identifier/mod.rs の FromStr）。ストリーム名・トピック名をそのまま使ってよい
// - POST /streams/{stream}/topics/{topic}/messages {"partitioning":{"kind":"partition_id","value":"AAAAAA=="},"messages":[{"id":0,"payload":"<base64>"}]}

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type iggyClient struct {
	baseURL string
	pat     string
	stream  string
	expiry  string
	http    *http.Client
	token   string
}

func newIggyClient(baseURL, pat, stream, expiry string) *iggyClient {
	return &iggyClient{baseURL: strings.TrimRight(baseURL, "/"), pat: pat, stream: stream, expiry: expiry,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// PAT / Bearer を別ホスト・別スキームへ転送しないよう redirect は一切追わない
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}
}

// ValidateIggyURL 資格情報を平文で送らないための事前検査。https か、ループバック宛の http だけを許す
func ValidateIggyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("iggy url: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("iggy url: %q は平文 http のリモート宛です。https か 127.0.0.1 を使ってください", raw)
	default:
		return fmt.Errorf("iggy url: 未対応のスキーム %q", u.Scheme)
	}
}

var errUnauthorized = errors.New("iggy: unauthorized")

func (c *iggyClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, out, nil
}

func (c *iggyClient) login(ctx context.Context) error {
	c.token = ""
	code, body, err := c.do(ctx, http.MethodPost, "/personal-access-tokens/login", map[string]string{"token": c.pat})
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("iggy login: status %d", code)
	}
	var v struct {
		AccessToken struct {
			Token string `json:"token"`
		} `json:"access_token"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.AccessToken.Token == "" {
		return fmt.Errorf("iggy login: bad response")
	}
	c.token = v.AccessToken.Token
	return nil
}

func (c *iggyClient) ensureStream(ctx context.Context) error {
	code, _, err := c.do(ctx, http.MethodGet, "/streams/"+c.stream, nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return errUnauthorized
	}
	if code == http.StatusOK {
		return nil
	}
	code, _, err = c.do(ctx, http.MethodPost, "/streams", map[string]any{"name": c.stream})
	if err != nil {
		return err
	}
	if code/100 == 2 || code == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("iggy create stream: status %d", code)
}

// expiryJSONValue message_expiry はマイクロ秒の JSON 数値（文字列不可、Step 0 参照）。
// c.expiry は NewEmitter で検証済みのため、ここでの ParseUint は失敗しない
func (c *iggyClient) expiryJSONValue() uint64 {
	n, _ := strconv.ParseUint(c.expiry, 10, 64)
	return n
}

func (c *iggyClient) ensureTopic(ctx context.Context, topic string) error {
	code, _, err := c.do(ctx, http.MethodGet, "/streams/"+c.stream+"/topics/"+topic, nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return errUnauthorized
	}
	if code == http.StatusOK {
		return nil
	}
	code, body, err := c.do(ctx, http.MethodPost, "/streams/"+c.stream+"/topics", map[string]any{
		"name": topic, "partitions_count": 1, "compression_algorithm": "none", "max_topic_size": 0, "message_expiry": c.expiryJSONValue(),
	})
	if err != nil {
		return err
	}
	if code/100 == 2 || code == http.StatusConflict || bytes.Contains(bytes.ToLower(body), []byte("already exists")) {
		return nil
	}
	return fmt.Errorf("iggy create topic %s: status %d", topic, code)
}

// encodePayloadForIggy 1 行を base64 にする。64MB 超は payload を切り詰め形に置き換える
func encodePayloadForIggy(line []byte) (string, bool) {
	if len(line) <= MaxPayloadBytes {
		return base64.StdEncoding.EncodeToString(line), false
	}
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return base64.StdEncoding.EncodeToString(line[:MaxPayloadBytes]), true
	}
	t, err := Truncated(e, len(line))
	if err != nil {
		return "", true
	}
	b, _ := t.Marshal()
	return base64.StdEncoding.EncodeToString(b), true
}

func (c *iggyClient) send(ctx context.Context, topic string, lines [][]byte) error {
	msgs := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		enc, _ := encodePayloadForIggy(l)
		msgs = append(msgs, map[string]any{"id": 0, "payload": enc})
	}
	body := map[string]any{
		"partitioning": map[string]any{"kind": "partition_id", "value": "AAAAAA=="},
		"messages":     msgs,
	}
	code, _, err := c.do(ctx, http.MethodPost, "/streams/"+c.stream+"/topics/"+topic+"/messages", body)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return errUnauthorized
	}
	if code/100 != 2 {
		return fmt.Errorf("iggy send: status %d", code)
	}
	return nil
}

type sender struct {
	dir    string
	runID  string
	client *iggyClient
	wake   chan struct{}
	done   chan struct{}
}

// withAuth 401 のとき 1 回だけ再ログインして f をやり直す
func (s *sender) withAuth(ctx context.Context, f func() error) error {
	if s.client.token == "" {
		if err := s.client.login(ctx); err != nil {
			return err
		}
	}
	err := f()
	if !errors.Is(err, errUnauthorized) {
		return err
	}
	if err := s.client.login(ctx); err != nil {
		return err
	}
	return f()
}

const sendBatch = 64

// drainRun 指定 run の全 WAL をカーソル位置から送る。完全な行を送り終えた直後にだけカーソルを進める
func (s *sender) drainRun(ctx context.Context, sessionDirs []string, runID string) error {
	if err := s.withAuth(ctx, func() error { return s.client.ensureStream(ctx) }); err != nil {
		return err
	}
	for _, sess := range sessionDirs {
		wp := walPath(s.dir, sess, runID)
		if _, err := os.Stat(wp); err != nil {
			continue
		}
		cp := cursorPath(s.dir, sess, runID)
		if err := s.withAuth(ctx, func() error { return s.client.ensureTopic(ctx, sess) }); err != nil {
			return err
		}
		for {
			off := readCursor(cp)
			recs, err := readFrom(wp, off, sendBatch)
			if err != nil {
				return err
			}
			if len(recs) == 0 {
				break
			}
			lines := make([][]byte, len(recs))
			for i, r := range recs {
				lines[i] = r.Line
			}
			if err := s.withAuth(ctx, func() error { return s.client.send(ctx, sess, lines) }); err != nil {
				return err
			}
			if err := writeCursor(cp, recs[len(recs)-1].End); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *sender) sessionDirs() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// scanDeadRuns 自分以外の run の lock を試し、取れた（死んだ）run を送って削除する
func (s *sender) scanDeadRuns(ctx context.Context) {
	locks, _ := filepath.Glob(filepath.Join(s.dir, "*.lock"))
	sessions := s.sessionDirs()
	for _, lp := range locks {
		runID := strings.TrimSuffix(filepath.Base(lp), ".lock")
		if runID == s.runID {
			continue
		}
		f, ok := tryLockRun(s.dir, runID)
		if !ok {
			continue
		}
		if err := s.drainRun(ctx, sessions, runID); err != nil {
			slog.Warn("audit: drain dead run failed", "run_id", runID, "err", err)
			_ = f.Close()
			continue
		}
		for _, sess := range sessions {
			_ = os.Remove(walPath(s.dir, sess, runID))
			_ = os.Remove(cursorPath(s.dir, sess, runID))
		}
		_ = f.Close()
		_ = os.Remove(lp)
	}
}

// run 送信ループ。wake か 1 秒ごとに自 run を drain し、失敗時は指数バックオフ（最大 30 秒）
func (s *sender) run(ctx context.Context) {
	defer close(s.done)
	// scanDeadRuns と通常ループの drainRun は呼び出し元 ctx で中断させない。中断すると
	// 「サーバ側は処理済みだがクライアントは失敗と判断」のケースが生まれ、次の送信（ここでは
	// 直後の shutdown drain）が同じバッチを二重送信する。ループの停止判定は下の select の
	// ctx.Done() 側だけで行い、実際の送信は HTTP クライアントのタイムアウトにのみ委ねる
	//nolint:contextcheck // 上記理由により意図的に独立させる
	s.scanDeadRuns(context.Background())
	backoff := time.Second
	for {
		//nolint:contextcheck // 上記理由により意図的に独立させる
		err := s.drainRun(context.Background(), s.sessionDirs(), s.runID)
		if err != nil {
			slog.Warn("audit: send failed, will retry", "err", err, "backoff", backoff)
		} else {
			backoff = time.Second
		}
		wait := time.Second
		if err != nil {
			wait = backoff
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
		// backoff 中 (err != nil) は wake を無効化する。有効なままだと emit の
		// たびに wake が飛び、失敗中のリトライ間隔が実質縮まってしまう
		var wake <-chan struct{}
		if err == nil {
			wake = s.wake
		}
		select {
		case <-ctx.Done():
			// ctx が閉じた後の最終ドレインは shutdown 猶予として意図的に独立させる
			//nolint:contextcheck // shutdown drain: ctx はもう Done なので新しい context で送り切る
			_ = s.drainRun(context.Background(), s.sessionDirs(), s.runID)
			return
		case <-wake:
		case <-time.After(wait):
		}
	}
}
