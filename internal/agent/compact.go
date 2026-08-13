package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// compactionSystemPrompt 要約器へ渡す system プロンプト
const compactionSystemPrompt = `あなたは会話履歴の要約器です。ユーザーメッセージに含まれる会話ログを、後続のやり取りで文脈として使える要約に変換します。
要約は日本語で書きます。次の 4 点を必ず含めます。
1. ユーザーが達成しようとしている目的
2. これまでに決定した事項
3. 実行したツール操作とその結果の要点
4. 未解決の課題
出力は要約文のみとし、挨拶や前置き、確認の質問文を含めません。`

// compactionUserPromptPrefix 会話ログの前に置く指示
const compactionUserPromptPrefix = "次の会話履歴を要約してください。\n\n---\n"

// compactionUserPromptSuffix 会話ログの後に置く区切り
const compactionUserPromptSuffix = "---\n"

// compactionSummaryLabel 要約本文の前に付ける見出し
const compactionSummaryLabel = "[過去の会話の要約]\n\n"

// compactionSummarySeparator 要約本文と結合先の user 発話を区切る
const compactionSummarySeparator = "\n\n[ここから現在の会話]\n\n"

// CompactOptions CompactMessages の圧縮パラメータ
type CompactOptions struct {
	// KeepRecentTurns 要約対象から除外し、そのまま保持する直近ターン数。
	// 1 ターンは user メッセージ 1 件から始まり、次の user メッセージの直前までの
	// assistant/tool メッセージ列を含む
	KeepRecentTurns int
}

// CompactMessages msgs を要約置換した新しいスライスを返す。
// 保持対象は (a) 先頭から連続する system メッセージ全て、(b) 直近
// opts.KeepRecentTurns 件の user ターン。それより古い区間を要約器に渡す。
// 戻ってきた要約文は、保持する先頭の user メッセージの content 先頭へ結合する。
// 独立した user メッセージとして挿入しないのは、要約の直後が必ず user
// メッセージになり role が user のメッセージが連続するためである
// (llama-server はこれを履歴検証で拒否し、以後の全リクエストが 400 で
// 失敗し続ける)。
// 保持ターンが 1 件も無い場合 (KeepRecentTurns <= 0) に限り、要約を末尾の
// 独立した RoleUser メッセージとして置く。
// 圧縮対象区間が空の場合は msgs をそのまま返し、要約呼び出しは行わない。
// prov.Chat が失敗した場合、または要約結果が空文字列の場合はエラーを返す
func CompactMessages(ctx context.Context, prov llm.Provider, model string, msgs []llm.Message, opts CompactOptions) ([]llm.Message, error) {
	keep := max(opts.KeepRecentTurns, 0)
	sysEnd := leadingSystemCount(msgs)
	turnStarts := userTurnStarts(msgs, sysEnd)

	var cutIdx int
	switch {
	case keep <= 0:
		cutIdx = len(msgs)
	case keep >= len(turnStarts):
		return msgs, nil // 保持対象ターン数が実際のターン数以上 = 圧縮不要
	default:
		cutIdx = turnStarts[len(turnStarts)-keep]
	}
	if sysEnd >= cutIdx {
		return msgs, nil // 要約対象区間が空
	}

	summary, err := summarizeTranscript(ctx, prov, model, renderTranscript(msgs[sysEnd:cutIdx]))
	if err != nil {
		return nil, err
	}

	result := make([]llm.Message, 0, sysEnd+1+(len(msgs)-cutIdx))
	result = append(result, msgs[:sysEnd]...)
	if cutIdx >= len(msgs) {
		// 保持ターンが無い。要約を末尾の独立した user メッセージとして置く
		return append(result, llm.Message{Role: llm.RoleUser, Content: compactionSummaryLabel + summary}), nil
	}
	// msgs[cutIdx] は保持する先頭の user メッセージ。要約をその content 先頭へ
	// 結合し、user メッセージの連続を発生させない
	head := msgs[cutIdx]
	head.Content = compactionSummaryLabel + summary + compactionSummarySeparator + head.Content
	result = append(result, head)
	return append(result, msgs[cutIdx+1:]...), nil
}

// summarizeTranscript 会話ログを要約器へ渡し、空でない要約文を得る
func summarizeTranscript(ctx context.Context, prov llm.Provider, model, transcript string) (string, error) {
	resp, err := prov.Chat(ctx, llm.ChatRequest{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactionSystemPrompt},
			{Role: llm.RoleUser, Content: compactionUserPromptPrefix + transcript + compactionUserPromptSuffix},
		},
	})
	if err != nil {
		return "", fmt.Errorf("compact: summarize call failed: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("compact: summarizer returned no response")
	}
	summary := strings.TrimSpace(resp.Message.Content)
	if summary == "" {
		return "", fmt.Errorf("compact: summarizer returned empty content")
	}
	return summary, nil
}

// leadingSystemCount 先頭から連続する RoleSystem メッセージの件数を返す
func leadingSystemCount(msgs []llm.Message) int {
	n := 0
	for n < len(msgs) && msgs[n].Role == llm.RoleSystem {
		n++
	}
	return n
}

// userTurnStarts from 以降にある RoleUser メッセージの index を昇順で返す
func userTurnStarts(msgs []llm.Message, from int) []int {
	var starts []int
	for i := from; i < len(msgs); i++ {
		if msgs[i].Role == llm.RoleUser {
			starts = append(starts, i)
		}
	}
	return starts
}

// renderTranscript 要約器に渡す平文の会話ログを組み立てる
func renderTranscript(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			b.WriteString("USER: " + m.Content + "\n")
		case llm.RoleAssistant:
			b.WriteString("ASSISTANT: " + m.Content)
			for _, tc := range m.ToolCalls {
				b.WriteString("\n  [tool_call " + tc.Name + "]")
			}
			b.WriteByte('\n')
		case llm.RoleTool:
			b.WriteString("TOOL[" + m.Name + "]: " + m.Content + "\n")
		case llm.RoleSystem:
			// renderTranscript は sysEnd 以降だけを受け取るため通常到達しない
		}
	}
	return b.String()
}
