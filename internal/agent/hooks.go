package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// defaultHookTimeout HookSpec.Timeout 未指定 (0) のときの既定値
const defaultHookTimeout = 10 * time.Second

// HookSpec 1 件の hook 実行仕様。config.HookConfig から変換して渡す
type HookSpec struct {
	Matcher string
	Command string
	// Timeout 0 なら defaultHookTimeout を使う
	Timeout time.Duration
}

// HookRunner pre/post hook の実行を担う
type HookRunner struct {
	pre  []HookSpec
	post []HookSpec
}

// NewHookRunner pre/post の HookSpec 一覧から HookRunner を構築する。
// pre・post がともに空の場合も nil を返さず空の HookRunner を返す
func NewHookRunner(pre, post []HookSpec) *HookRunner {
	return &HookRunner{pre: pre, post: post}
}

type hookPayload struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	// Result pre では nil (JSON にも出ない)
	Result *hookResult `json:"result,omitempty"`
}

// hookResult post hook へ渡すツール実行結果。pre hook では渡さない
type hookResult struct {
	IsError    bool   `json:"is_error"`
	Content    string `json:"content"`
	DurationMS int64  `json:"duration_ms"`
}

// HookResult RunPost の引数として loop.go / parallel.go が組み立てる公開型
type HookResult struct {
	// IsError ツールがエラーを返したか
	IsError bool
	// Content ツール結果本文。untrusted マーカー付与前・redaction 前の生の値
	Content string
	// Duration ツール実行に要した時間
	Duration time.Duration
}

// RunPre matcher に一致する pre hook を config 記載順に実行する。
// いずれかが exit 2 で拒否した場合、即座に allowed=false とその stderr を reason
// として返し、以降の hook は実行しない。
// timeout・その他の非 0 exit code は警告ログのみで次の hook へ進む (fail-open)。
// 親 ctx のキャンセル (ESC 中断・SIGINT) は fail-open の対象外で拒否として扱う
func (h *HookRunner) RunPre(ctx context.Context, toolName string, args json.RawMessage) (allowed bool, reason string) {
	if h == nil {
		return true, ""
	}
	for _, spec := range h.pre {
		if !hookMatches(spec.Matcher, toolName) {
			continue
		}
		res, runErr := runHook(ctx, spec, hookPayload{Tool: toolName, Args: args})
		if runErr != nil {
			slog.WarnContext(ctx, "pre_tool_use hook failed to start; allowing", "tool", toolName, "matcher", spec.Matcher, "err", runErr)
			continue
		}
		if res.parentCanceled {
			// ユーザーによる明示的な中断。fail-open は hook 側の実装バグや
			// 外部依存先の遅延に対する措置であり、明示的な中断はその対象ではない
			return false, "interrupted"
		}
		switch res.exitCode {
		case 0:
			continue
		case 2:
			return false, res.stderr
		default:
			slog.WarnContext(ctx, "pre_tool_use hook exited non-zero (not 2); allowing", "tool", toolName, "matcher", spec.Matcher, "exit_code", res.exitCode, "stderr", res.stderr)
		}
	}
	return true, ""
}

// RunPost matcher に一致する post hook を config 記載順に実行する。
// stdin payload には result (成否・本文・実行時間) を含める。
// exit code・timeout を一切解釈せず、失敗は警告ログのみに留める
func (h *HookRunner) RunPost(ctx context.Context, toolName string, args json.RawMessage, result HookResult) {
	if h == nil {
		return
	}
	payload := hookPayload{
		Tool: toolName,
		Args: args,
		Result: &hookResult{
			IsError:    result.IsError,
			Content:    result.Content,
			DurationMS: result.Duration.Milliseconds(),
		},
	}
	for _, spec := range h.post {
		if !hookMatches(spec.Matcher, toolName) {
			continue
		}
		res, runErr := runHook(ctx, spec, payload)
		if runErr != nil {
			slog.WarnContext(ctx, "post_tool_use hook failed to start", "tool", toolName, "matcher", spec.Matcher, "err", runErr)
			continue
		}
		if res.exitCode != 0 {
			slog.WarnContext(ctx, "post_tool_use hook exited non-zero", "tool", toolName, "matcher", spec.Matcher, "exit_code", res.exitCode, "stderr", res.stderr)
		}
	}
}

// hookMatches matcher はツール名の完全一致、または任意ツールに一致する "*"
func hookMatches(matcher, toolName string) bool {
	return matcher == "*" || matcher == toolName
}

// hookOutcome runHook の結果
type hookOutcome struct {
	exitCode int
	stdout   string
	stderr   string
	// parentCanceled 親 ctx のキャンセル。hook 自身の timeout とは区別する
	parentCanceled bool
}

// runHook 1 件の hook を sh -c で起動し、stdin に payload JSON を渡す。
// 環境変数 GO_LLM_AGENT_TOOL=payload.Tool を既存環境へ追加する。
// hook 自身の timeout 超過は exitCode=-1 / stderr="timeout after Ns" として返し、
// 呼び出し元の「非 0 exit code」分岐に載せる。
// 親 ctx のキャンセルは parentCanceled=true として timeout とは別に扱う。
// 両者を同じ timeout として分類すると、ユーザーが明示的に中断した瞬間に走っていた
// pre hook が fail-open で許可判定として扱われる
func runHook(ctx context.Context, spec HookSpec, payload hookPayload) (hookOutcome, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return hookOutcome{}, fmt.Errorf("hooks: marshal payload: %w", err)
	}

	cmd := exec.CommandContext(hctx, "sh", "-c", spec.Command)
	cmd.Stdin = bytes.NewReader(body)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Env = append(os.Environ(), "GO_LLM_AGENT_TOOL="+payload.Tool)

	runErr := cmd.Run()
	out := hookOutcome{stdout: stdoutBuf.String(), stderr: stderrBuf.String()}
	if runErr == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		out.parentCanceled = true
		out.exitCode = -1
		out.stderr = "canceled by parent context"
		return out, nil
	}
	if hctx.Err() != nil {
		out.exitCode = -1
		out.stderr = fmt.Sprintf("timeout after %s", timeout)
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		out.exitCode = exitErr.ExitCode()
		return out, nil
	}
	return hookOutcome{}, fmt.Errorf("hooks: run %q: %w", spec.Command, runErr)
}
