package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/okamyuji/go-llm-agent/internal/eval"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

// oneShotParams run サブコマンドの実行パラメータ。Out は nil で os.Stdout になる
type oneShotParams struct {
	ConfigPath string
	Model      string
	Prompt     string
	Out        io.Writer
}

// runOneShot 単発プロンプトを 1 ターン実行して結果を書き出す
func runOneShot(ctx context.Context, p oneShotParams) error {
	deps, err := buildServiceDeps(ctx, p.ConfigPath, p.Model, false)
	if err != nil {
		return err
	}
	out := p.Out
	if out == nil {
		out = os.Stdout
	}
	return cliui.RunOneShot(ctx, deps.svc, deps.model, deps.cfg.Agent.SystemPrompt, p.Prompt, deps.cfg.Agent.MaxToolHops, out)
}

// serveParams serve サブコマンドの実行パラメータ。Addr が空なら config の値を使う
type serveParams struct {
	ConfigPath string
	Addr       string
}

// runServer HTTP API サーバーを起動する。
// serve のみ HTTPApprover を /v1/runs/<id>/approve に渡す。
// non-stream の最終 content に再適用する Redactor も渡す
// (loop の chunk-by-chunk redact だけだと PII が chunk 境界を跨いで取りこぼされる)
func runServer(ctx context.Context, p serveParams) error {
	deps, err := buildServiceDeps(ctx, p.ConfigPath, "", true)
	if err != nil {
		return err
	}
	addr := p.Addr
	if addr == "" {
		addr = deps.cfg.Server.Addr
	}
	rd, rerr := buildRedactor(deps.cfg)
	if rerr != nil {
		return fmt.Errorf("build safety redactor: %w", rerr)
	}
	return httpapi.ListenAndServe(ctx, addr, deps.svc, deps.cfg, deps.acc, deps.approver, rd)
}

// listTools 有効化されているツールの一覧を書き出す
func listTools(ctx context.Context, configPath string, out io.Writer) error {
	// telemetry / 親 ctx の cancellation を伝搬させるため、サブコマンド受領 ctx をそのまま渡す
	_, _, tools, _, _, err := loadDeps(ctx, configPath, false)
	if err != nil {
		return err
	}
	for _, s := range tools.List() {
		fmt.Fprintf(out, "- %s  %s\n", s.Name, s.Description)
	}
	return nil
}

// evalParams eval サブコマンドの実行パラメータ。Out は nil で os.Stdout になる
type evalParams struct {
	ConfigPath string
	Suite      string
	Report     string
	Model      string
	Out        io.Writer
}

// runEvalSuite suite ディレクトリの YAML を読み込み agent.Service で実行してレポートを書く
func runEvalSuite(ctx context.Context, p evalParams) error {
	deps, err := buildServiceDeps(ctx, p.ConfigPath, p.Model, false)
	if err != nil {
		return err
	}
	cases, err := eval.LoadSuite(p.Suite)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("no eval cases found under %s", p.Suite)
	}
	results, err := eval.RunSuite(ctx, deps.svc, cases, deps.model, deps.cfg.Agent.MaxToolHops)
	if err != nil {
		return err
	}
	return reportEvalResults(p.Report, cases, results, p.Out)
}

// reportEvalResults 採点結果を JSON レポートへ書き出し、1 件でも失敗があれば error を返す
func reportEvalResults(reportPath string, cases []eval.Case, results []eval.RunResult, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	scores := make([]eval.Scores, len(cases))
	for i := range cases {
		scores[i] = eval.Score(cases[i], results[i])
	}
	if err := eval.WriteReport(reportPath, cases, results, scores); err != nil {
		return err
	}
	var passed int
	for _, s := range scores {
		if s.Passed {
			passed++
		}
	}
	fmt.Fprintf(out, "eval report: %d/%d passed (report: %s)\n", passed, len(cases), reportPath)
	if passed != len(cases) {
		return fmt.Errorf("%d eval case(s) failed", len(cases)-passed)
	}
	return nil
}
