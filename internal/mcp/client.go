// Package mcp Model Context Protocol の最小クライアント
// stdio 経由で JSON-RPC を交換し、tools/list と tools/call を発行する
// 12 番設計書 MVP として SSE は未実装で、stdio のみサポートする
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// validateMCPCommand mcp.servers.<name>.command[0] が許容パス形式かを検査する
// 受け付けるのは絶対パスのみ、かつ正規化後に .. セグメントを含まないものに限定する
// PATH 経由の任意コマンド実行 (例: "node") と path traversal (例: "/opt/srv/../../etc/passwd") を防ぐ
func validateMCPCommand(path string) error {
	if path == "" {
		return errors.New("mcp: command[0] is empty")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("mcp: command[0] must be an absolute path, got %q", path)
	}
	cleaned := filepath.Clean(path)
	// Clean 後でも .. が残るのは現在ディレクトリより上を参照しているケース。Clean が
	// 絶対パスの先頭境界を越える .. は除去するが、保守のため明示的に再確認する
	if hasDotDotSegment(cleaned) {
		return fmt.Errorf("mcp: command[0] must not contain .. segments, got %q", path)
	}
	return nil
}

// hasDotDotSegment cleaned パスに ".." セグメントが含まれるかを判定する
// strings.SplitSeq + slices.Contains は短いシーケンス操作向けの簡潔形
func hasDotDotSegment(p string) bool {
	return slices.Contains(slices.Collect(strings.SplitSeq(p, string(filepath.Separator))), "..")
}

// ToolInfo discovery 結果に現れるツール 1 件
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// CallResult tools/call の戻り値
type CallResult struct {
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

// Client MCP の最小 JSON-RPC クライアント
// closed フラグは ctx キャンセルまたは Close() 後に true となり、call() で書き込み前に確認する
type Client struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Scanner
	mu     sync.Mutex
	closed bool
	nextID atomic.Int64
}

// ErrClientClosed 既に閉じられたクライアントに対する call で返るエラー
var ErrClientClosed = errors.New("mcp: client is closed")

// NewStdioClient stdio transport の Client を起動する
// command の最初の要素を exec し、stdin/stdout を JSON-RPC line として使う
// command[0] は絶対パスのみ許容する (PATH 解決による任意コマンド実行を防ぐ)
// 加えて filepath.Clean 後の path に .. セグメントが含まれていないことも保証する
func NewStdioClient(ctx context.Context, command []string) (*Client, error) {
	if len(command) == 0 {
		return nil, errors.New("mcp: command is empty")
	}
	if err := validateMCPCommand(command[0]); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	// stderr が読み取られないとパイプバッファが満杯になり子プロセスがハングする
	// MCP サーバ側のログを失わないよう親プロセスの stderr に流す
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp start: %w", err)
	}
	sc := bufio.NewScanner(out)
	// 大きな tools/list 応答（多数の tool スキーマ）に耐えるため、初期 64KiB から
	// 最大 16MiB まで自動拡張する設定にする
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &Client{cmd: cmd, in: in, out: sc}, nil
}

// Close stdin を閉じて子プロセスを終了させる
// stdin.Close と cmd.Wait のエラーを errors.Join で集約して返す
// closed フラグを true にしてから ctx キャンセルや併走 call() が pipe に書き込まないようにする
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	in := c.in
	c.in = nil
	c.mu.Unlock()
	var errs []error
	if in != nil {
		if err := in.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.cmd != nil {
		if err := c.cmd.Wait(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rpcRequest JSON-RPC 2.0 リクエスト
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 レスポンス
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError JSON-RPC エラー本体
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call JSON-RPC を 1 往復する
// 並列呼び出しは mu で直列化する。実用は十分なシリアル化粒度。
// ctx 経由のキャンセルは I/O ブロック中の中断を実現するため、リクエスト送信前と
// 応答受信を別 goroutine で待つことで実現する。MVP 範囲のシンプルな実装
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 既に Close() または ctx キャンセル経由で stdin が閉じられている場合は早期返却し、
	// nil パイプへの書き込みによる panic を防ぐ
	if c.closed || c.in == nil {
		return nil, ErrClientClosed
	}
	id := c.nextID.Add(1)
	var p json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp marshal params: %w", err)
		}
		p = b
	}
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: p}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp marshal: %w", err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("mcp write: %w", err)
	}

	type scanResult struct {
		ok  bool
		err error
		raw []byte
	}
	rch := make(chan scanResult, 1)
	go func() {
		if c.out.Scan() {
			// Scanner の内部バッファは次回 Scan で上書きされるためコピーする
			line := append([]byte(nil), c.out.Bytes()...)
			rch <- scanResult{ok: true, raw: line}
			return
		}
		rch <- scanResult{ok: false, err: c.out.Err()}
	}()
	var sr scanResult
	select {
	case <-ctx.Done():
		// ctx キャンセル時は scanner goroutine がブロックしたままになる
		// stdin を閉じて子プロセスを EOF 終了させ、Scan の戻りを促してから
		// rch を回収することで goroutine leak を防ぐ
		// 以降の call() は closed フラグ確認で ErrClientClosed を返すよう設計してある
		// 関数冒頭の c.mu.Lock() を defer で保持しているため、ここで c.in / c.closed を
		// 書き換える操作は Close() および他の call() と race しない
		if c.in != nil {
			_ = c.in.Close()
			c.in = nil
		}
		c.closed = true
		<-rch
		return nil, ctx.Err()
	case sr = <-rch:
	}
	if !sr.ok {
		if sr.err != nil {
			return nil, fmt.Errorf("mcp read: %w", sr.err)
		}
		return nil, errors.New("mcp: server closed connection")
	}
	var resp rpcResponse
	if err := json.Unmarshal(sr.raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp unmarshal: %w", err)
	}
	if resp.ID != id {
		return nil, fmt.Errorf("mcp: response id mismatch want=%d got=%d", id, resp.ID)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// ListTools tools/list を呼び ToolInfo の配列を返す
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	res, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(res, &body); err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	return body.Tools, nil
}

// Call tools/call を呼ぶ
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: args}
	res, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	var r CallResult
	if err := json.Unmarshal(res, &r); err != nil {
		return CallResult{}, fmt.Errorf("mcp tools/call: %w", err)
	}
	return r, nil
}
