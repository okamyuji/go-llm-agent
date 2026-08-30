package cliui

import (
	"fmt"
	"io"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/memory"
)

// hashMemoryFileName # プレフィックスの保存先トピックファイル
const hashMemoryFileName = "memories.md"

// memoryDisplayMaxBytes /memory で表示する 1 ファイルの上限
const memoryDisplayMaxBytes = 64 * 1024

// memoryDisabledMessage MemoryStore 未設定時の案内
const memoryDisabledMessage = "[memory] メモリ機能は無効です (agent.memory.enabled または初期化失敗を確認してください)"

// handleMemoryCommand /memory を処理する。arg が空なら一覧と索引を、
// ファイル名ならその本文を表示する。表示のみで編集はしない
func (r *REPL) handleMemoryCommand(arg string, out io.Writer) {
	st := r.opt.MemoryStore
	if st == nil {
		fmt.Fprintln(out, memoryDisabledMessage)
		return
	}
	if arg != "" {
		body, err := st.Read(arg, memoryDisplayMaxBytes)
		if err != nil {
			fmt.Fprintf(out, "[memory] %s を読めません: %v\n", arg, err)
			return
		}
		fmt.Fprintf(out, "[memory] %s:\n%s\n", arg, body)
		return
	}
	names, err := st.List()
	if err != nil {
		fmt.Fprintf(out, "[memory] 一覧を取得できません: %v\n", err)
		return
	}
	fmt.Fprintf(out, "[memory] ファイル (%d 件): %s\n", len(names), strings.Join(names, " "))
	index, err := st.ReadIndex(0, memoryDisplayMaxBytes)
	if err != nil {
		fmt.Fprintf(out, "[memory] 索引を読めません: %v\n", err)
		return
	}
	if index == "" {
		fmt.Fprintln(out, "[memory] 索引 MEMORY.md はまだありません")
		return
	}
	fmt.Fprintf(out, "[memory] %s:\n%s\n", memory.IndexFileName, index)
}

// handleHashMemory `# <本文>` を memories.md と索引 MEMORY.md へ 1 行ずつ追記する
func (r *REPL) handleHashMemory(body string, out io.Writer) {
	st := r.opt.MemoryStore
	if st == nil {
		fmt.Fprintln(out, memoryDisabledMessage)
		return
	}
	if body == "" {
		fmt.Fprintln(out, "[memory] # の後に保存する本文を指定してください")
		return
	}
	line := "- " + body + "\n"
	if err := st.Write(hashMemoryFileName, line, true); err != nil {
		fmt.Fprintf(out, "[memory] 保存に失敗しました: %v\n", err)
		return
	}
	if err := st.Write(memory.IndexFileName, "- "+body+" ("+hashMemoryFileName+")\n", true); err != nil {
		fmt.Fprintf(out, "[memory] 索引の更新に失敗しました: %v\n", err)
		return
	}
	fmt.Fprintf(out, "[memory] %s へ保存しました\n", hashMemoryFileName)
}
