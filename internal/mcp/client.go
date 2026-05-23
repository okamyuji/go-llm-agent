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
	"os/exec"
	"sync"
	"sync/atomic"
)

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
type Client struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Scanner
	mu     sync.Mutex
	nextID atomic.Int64
}

// NewStdioClient stdio transport の Client を起動する
// command の最初の要素を exec し、stdin/stdout を JSON-RPC line として使う
func NewStdioClient(ctx context.Context, command []string) (*Client, error) {
	if len(command) == 0 {
		return nil, errors.New("mcp: command is empty")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
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
func (c *Client) Close() error {
	var errs []error
	if c.in != nil {
		if err := c.in.Close(); err != nil {
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
