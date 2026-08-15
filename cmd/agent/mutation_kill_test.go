package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// cmdRun/cmdServe/cmdTools/cmdEval/cmdConfig は flag 解析成功後に本体処理へ
// 進まなければならない。存在しない config を渡すと本体処理が必ずエラーを
// 返すため、nil が返る場合は解析直後に早期 return している (解析結果を
// 捨てている) ことを意味する。
func TestCmdDispatch_ParseSuccessReachesBody(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	args := []string{"-config", missing}
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() error
	}{
		{"run", func() error { return cmdRun(ctx, args) }},
		{"serve", func() error { return cmdServe(ctx, args) }},
		{"tools", func() error { return cmdTools(ctx, args) }},
		{"eval", func() error { return cmdEval(ctx, args) }},
		{"config", func() error { return cmdConfig(args) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Fatal("config 不在で本体処理のエラー期待")
			}
		})
	}
}

// initTelemetry は OTel 有効時に初期化へ進み、成功したら nil を返して
// shutdown フックを差し替える。HTTP exporter は接続を遅延するため、
// 到達不能な endpoint でも初期化自体は成功する。
func TestInitTelemetry_EnabledSucceedsAndSwapsShutdown(t *testing.T) {
	prev := shutdownTelemetry
	t.Cleanup(func() { shutdownTelemetry = prev })

	cfg := &config.Config{}
	cfg.Observability.OTel.Enabled = true
	cfg.Observability.OTel.Endpoint = "127.0.0.1:1"
	cfg.Observability.OTel.Insecure = true

	if err := initTelemetry(context.Background(), cfg); err != nil {
		t.Fatalf("有効時の初期化成功で nil 期待 got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 差し替わった shutdown を呼んで後始末する (エラーは到達不能起因で許容)
	_ = shutdownTelemetry(ctx)
}

// runEvalSuite はケース読込と実行が済んだら必ず reportEvalResults まで進む。
// 到達不能なプロバイダーでは全ケースが失敗し、レポートは書かれたうえで
// 失敗を告げるエラーが返る。nil が返る場合は RunSuite 直後に早期 return
// している (成功時に結果を捨てている) ことを意味する。
func TestRunEvalSuite_FailingCaseWritesReportAndErrors(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeChatConfig(t, dir, "")
	suite := filepath.Join(dir, "cases")
	if err := os.MkdirAll(suite, 0o755); err != nil {
		t.Fatal(err)
	}
	caseYAML := "id: c1\n" +
		"input:\n" +
		"  messages:\n" +
		"    - role: user\n" +
		"      content: say ok\n" +
		"expected:\n" +
		"  phrases: [\"ok\"]\n" +
		"metrics:\n" +
		"  phrase_recall_min: 1\n"
	if err := os.WriteFile(filepath.Join(suite, "c1.yaml"), []byte(caseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.json")
	err := runEvalSuite(context.Background(), evalParams{
		ConfigPath: cfgPath,
		Suite:      suite,
		Report:     report,
	})
	if err == nil || !strings.Contains(err.Error(), "eval") {
		t.Fatalf("失敗ケースを含む suite はエラー期待 got %v", err)
	}
	if _, statErr := os.Stat(report); statErr != nil {
		t.Fatalf("レポートは書かれている期待 got %v", statErr)
	}
}
