package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// DefaultShellArgDenyPatterns shell 引数の既定 deny 正規表現
// プロンプトインジェクション/サプライチェーン攻撃の典型経路を遮断する
var DefaultShellArgDenyPatterns = []string{
	`(^|\s)config\s+--global`,
	`(^|\s)config\s+--system`,
	`-c\s+core\.sshCommand`,
	`-c\s+http\.proxy`,
	`env\s+-w\b`,
	`(^|\s)install\b`,
	`-c\s+`,
	`--exec\b`,
	`\bsh\s+-c\b`,
	`\bbash\s+-c\b`,
}

// ShellTool shell ツールの実装
type ShellTool struct {
	cfg     config.ShellToolConfig
	logger  *slog.Logger
	allow   map[string]struct{}
	argDeny []*regexp.Regexp
}

// NewShell config と logger を受け取り ShellTool を生成する
func NewShell(cfg config.ShellToolConfig, logger *slog.Logger) *ShellTool {
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	if cfg.MaxTimeoutSeconds <= 0 {
		cfg.MaxTimeoutSeconds = 300
	}
	allow := map[string]struct{}{}
	for _, b := range cfg.AllowBinaries {
		allow[b] = struct{}{}
	}
	patterns := append([]string{}, DefaultShellArgDenyPatterns...)
	patterns = append(patterns, cfg.ArgDenyPatterns...)
	denies := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err == nil {
			denies = append(denies, re)
		}
	}
	return &ShellTool{cfg: cfg, logger: logger, allow: allow, argDeny: denies}
}

// Spec ツール定義を返す
func (t *ShellTool) Spec() Spec {
	return Spec{
		Name:        "shell",
		Description: "短命のシェルコマンドを実行する。allow_binaries に含まれるコマンドかつ arg_deny_patterns に該当しないもののみ実行可能",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
"command":{"type":"string","description":"argv の先頭バイナリ名"},
"args":{"type":"array","items":{"type":"string"}},
"timeout_seconds":{"type":"integer","minimum":1}
},
"required":["command"]
}`),
	}
}

type shellArgs struct {
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// Execute コマンドを許可リスト照合し引数 deny を判定してから実行する
func (t *ShellTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Command == "" {
		return Result{IsError: true, Content: "command is required"}, nil
	}
	base := filepath.Base(a.Command)
	if _, ok := t.allow[base]; !ok {
		t.audit(ctx, base, a.Args, 0, false, "denied_binary")
		return Result{IsError: true, Content: fmt.Sprintf("shell: %q は allow_binaries に含まれていません", base)}, nil
	}
	joined := strings.Join(a.Args, " ")
	for _, re := range t.argDeny {
		if re.MatchString(joined) {
			t.audit(ctx, base, a.Args, 0, false, "denied_args:"+re.String())
			return Result{IsError: true, Content: fmt.Sprintf("shell: 引数が deny パターンに一致しました (%s)", re.String())}, nil
		}
	}
	resolved, err := exec.LookPath(a.Command)
	if err != nil {
		t.audit(ctx, base, a.Args, 0, false, "lookup_failed")
		return Result{IsError: true, Content: fmt.Sprintf("shell: %v", err)}, nil
	}
	if filepath.Base(resolved) != base {
		t.audit(ctx, base, a.Args, 0, false, "binary_mismatch")
		return Result{IsError: true, Content: fmt.Sprintf("shell: 解決後のバイナリ名が一致しません %s vs %s", filepath.Base(resolved), base)}, nil
	}

	timeout := a.TimeoutSeconds
	if timeout <= 0 {
		timeout = t.cfg.TimeoutSeconds
	}
	if timeout > t.cfg.MaxTimeoutSeconds {
		timeout = t.cfg.MaxTimeoutSeconds
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(cctx, resolved, a.Args...)
	out, runErr := cmd.CombinedOutput()
	elapsed := time.Since(start)
	t.audit(ctx, base, a.Args, elapsed, runErr == nil, fmt.Sprintf("exit=%v", runErr))
	if runErr != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return Result{IsError: true, Content: fmt.Sprintf("shell timeout after %ds\n%s", timeout, string(out))}, nil
		}
		return Result{IsError: true, Content: fmt.Sprintf("shell: %v\n%s", runErr, string(out))}, nil
	}
	return Result{Content: string(out)}, nil
}

func (t *ShellTool) audit(ctx context.Context, binary string, args []string, elapsed time.Duration, ok bool, reason string) {
	if t.logger == nil {
		return
	}
	corr, _ := ctx.Value(correlationKey{}).(string)
	t.logger.Info("audit",
		slog.String("tool", "shell"),
		slog.String("binary", binary),
		slog.String("args", strings.Join(args, " ")),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.Bool("ok", ok),
		slog.String("reason", reason),
		slog.String("correlation_id", corr),
	)
}

// correlationKey は audit ログの相関 ID を取り出すための context key
type correlationKey struct{}

// CorrelationKey audit ログ間で hop を紐付ける context key
func CorrelationKey() any { return correlationKey{} }
