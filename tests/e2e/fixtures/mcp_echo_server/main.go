// Package main 12 設計書の MCP stdio JSON-RPC サーバの最小フィクスチャ
// tools/list と tools/call の 2 メソッドを実装し、echo ツールを公開する
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *err   `json:"error,omitempty"`
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
			continue
		}
		var resp rpcResp
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
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
			_ = json.Unmarshal(req.Params, &p)
			resp.Result = map[string]any{"content": fmt.Sprintf("echo: %s", string(p.Arguments)), "isError": false}
		default:
			resp.Error = &err{Code: -32601, Message: "method not found"}
		}
		b, _ := json.Marshal(resp)
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	if e := sc.Err(); e != nil {
		fmt.Fprintln(os.Stderr, "scanner err:", e)
	}
}
