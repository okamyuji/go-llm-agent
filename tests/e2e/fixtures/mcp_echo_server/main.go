// Package main 12 設計書の MCP stdio JSON-RPC サーバの最小フィクスチャ
// tools/list と tools/call の 2 メソッドを実装し、echo ツールを公開する
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// rpcReq JSON-RPC 2.0 仕様で id は string / number / null のいずれも許される
// 受信時に json.RawMessage で受け取って、応答時にそのまま返す
type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResp ID は json.RawMessage にする
// JSON-RPC 2.0 仕様では id は string / number / null のいずれも許され、
// Parse error 応答では明示的に "id": null を返す必要がある
// json.RawMessage を使うとリクエストの id 表現をそのままエコーできて型情報を維持できる
type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *err            `json:"error,omitempty"`
}

type err struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()
	for sc.Scan() {
		var req rpcReq
		if e := json.Unmarshal(sc.Bytes(), &req); e != nil {
			// JSON-RPC 2.0 Parse error (-32700) として明示的に応答する
			// Parse error 応答は spec に従い id を明示的に null で返す
			parseErr := rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &err{Code: -32700, Message: "parse error: " + e.Error()}}
			if b, merr := json.Marshal(parseErr); merr == nil {
				_, _ = out.Write(append(b, '\n'))
				_ = out.Flush()
			}
			continue
		}
		var resp rpcResp
		resp.JSONRPC = "2.0"
		if len(req.ID) > 0 {
			resp.ID = req.ID
		} else {
			resp.ID = json.RawMessage("null")
		}
		switch req.Method {
		case "tools/list":
			resp.Result = map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "echo back the input", "inputSchema": map[string]any{"type": "object"}},
				},
			}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if uerr := json.Unmarshal(req.Params, &p); uerr != nil {
				resp.Result = map[string]any{"content": "invalid params: " + uerr.Error(), "isError": true}
			} else {
				resp.Result = map[string]any{"content": fmt.Sprintf("echo: %s", string(p.Arguments)), "isError": false}
			}
		default:
			resp.Error = &err{Code: -32601, Message: "method not found"}
		}
		b, merr := json.Marshal(resp)
		if merr != nil {
			fmt.Fprintln(os.Stderr, "marshal resp:", merr)
			continue
		}
		if _, werr := out.Write(append(b, '\n')); werr != nil {
			fmt.Fprintln(os.Stderr, "write resp:", werr)
			continue
		}
		if ferr := out.Flush(); ferr != nil {
			fmt.Fprintln(os.Stderr, "flush:", ferr)
		}
	}
	if e := sc.Err(); e != nil {
		fmt.Fprintln(os.Stderr, "scanner err:", e)
	}
}
