package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/eval"
)

func TestRunOneShot_ConfigErrorPropagates(t *testing.T) {
	err := runOneShot(context.Background(), oneShotParams{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Prompt:     "hello",
		Out:        &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestRunOneShot_UnreachableProviderErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	var out bytes.Buffer
	// base_url は到達不能なため 1 ターン目で失敗する。ここで検証するのは
	// 依存の組み立てと出力先の配線が最後まで通ることである
	err := runOneShot(context.Background(), oneShotParams{ConfigPath: path, Prompt: "hi", Out: &out})
	if err == nil {
		t.Fatal("到達不能なプロバイダーはエラー期待")
	}
}

func TestRunServer_ConfigErrorPropagates(t *testing.T) {
	err := runServer(context.Background(), serveParams{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")})
	if err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestRunServer_StartsAndStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// ctx を先に打ち切ることで待受せずに戻る。addr 未指定時に config の値へ
	// フォールバックする経路もここで通る
	if err := runServer(ctx, serveParams{ConfigPath: path}); err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("ctx 打ち切りは正常終了かキャンセル通知期待 got %v", err)
	}
}

func TestListTools_PrintsEnabledTools(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	var out bytes.Buffer
	if err := listTools(context.Background(), path, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("既定で有効なツールが列挙される期待")
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") {
			t.Fatalf("各行が \"- \" で始まる期待 got %q", line)
		}
	}
}

func TestListTools_FormatsNameAndDescription(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	// enabled_tools を fs_read のみに差し替える
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), "enabled_tools: []", "enabled_tools: [\"fs_read\"]", 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listTools(context.Background(), path, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "- fs_read  ") {
		t.Fatalf("\"- 名前  説明\" 形式期待 got %q", out.String())
	}
}

func TestListTools_ConfigErrorPropagates(t *testing.T) {
	if err := listTools(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), &bytes.Buffer{}); err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestRunEvalSuite_ConfigErrorPropagates(t *testing.T) {
	err := runEvalSuite(context.Background(), evalParams{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Suite:      t.TempDir(),
	})
	if err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestRunEvalSuite_EmptySuiteErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	suite := t.TempDir()
	err := runEvalSuite(context.Background(), evalParams{ConfigPath: path, Suite: suite, Report: filepath.Join(dir, "r.json")})
	if err == nil || !strings.Contains(err.Error(), "no eval cases found under") {
		t.Fatalf("ケース 0 件のエラーメッセージ期待 got %v", err)
	}
}

func TestRunEvalSuite_MissingSuiteDirErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	err := runEvalSuite(context.Background(), evalParams{
		ConfigPath: path,
		Suite:      filepath.Join(dir, "no-such-dir"),
		Report:     filepath.Join(dir, "r.json"),
	})
	if err == nil {
		t.Fatal("suite ディレクトリ不在はエラー期待")
	}
}

// evalFixture 指定した合否になる 1 件分のケースと結果を作る
func evalFixture(pass bool) ([]eval.Case, []eval.RunResult) {
	cases := []eval.Case{{
		ID:       "c1",
		Expected: eval.CaseExpected{Phrases: []string{"ok"}},
		Metrics:  eval.CaseMetrics{PhraseRecallMin: 1},
	}}
	answer := "ok"
	if !pass {
		answer = "ng"
	}
	return cases, []eval.RunResult{{CaseID: "c1", FinalText: answer}}
}

func TestReportEvalResults_AllPassed(t *testing.T) {
	cases, results := evalFixture(true)
	report := filepath.Join(t.TempDir(), "report.json")
	var out bytes.Buffer
	if err := reportEvalResults(report, cases, results, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "eval report: 1/1 passed") {
		t.Fatalf("合格件数の出力期待 got %q", out.String())
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("レポートが JSON である期待 got %v", err)
	}
}

func TestReportEvalResults_FailureReturnsError(t *testing.T) {
	cases, results := evalFixture(false)
	report := filepath.Join(t.TempDir(), "report.json")
	err := reportEvalResults(report, cases, results, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "1 eval case(s) failed") {
		t.Fatalf("失敗件数のエラー期待 got %v", err)
	}
}

func TestReportEvalResults_WriteErrorPropagates(t *testing.T) {
	cases, results := evalFixture(true)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := reportEvalResults(filepath.Join(blocker, "report.json"), cases, results, &bytes.Buffer{})
	if err == nil {
		t.Fatal("レポート書き出し失敗はエラー期待")
	}
}

// TestCmdRun_ImageLoadErrorStopsBeforeConfig -image の読み込み失敗は設定読み込みより先に返り、LLM を呼ばない
func TestCmdRun_ImageLoadErrorStopsBeforeConfig(t *testing.T) {
	dir := t.TempDir()
	err := cmdRun(context.Background(), []string{
		"-config", filepath.Join(dir, "missing.yaml"),
		"-image", filepath.Join(dir, "missing.png"),
		"-p", "q",
	})
	if err == nil || !strings.HasPrefix(err.Error(), "image:") {
		t.Errorf("err = %v, want image load error", err)
	}
}

// TestRunOneShot_SendsImageToProvider -image の画像が設定済み provider へ content parts として届く
func TestRunOneShot_SendsImageToProvider(t *testing.T) {
	// 監査を有効にすると利用者のホームへ WAL を書くため、このテストでは止める
	t.Setenv("IGGY_PAT", "")
	var gotBody struct {
		Messages []map[string]any `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"red\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	cfg, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(cfg, []byte("http://127.0.0.1:1"), []byte(srv.URL), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "a.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runOneShot(t.Context(), oneShotParams{ConfigPath: path, Prompt: "q", ImagePath: img, Out: &out}); err != nil {
		t.Fatalf("runOneShot: %v", err)
	}
	if !strings.Contains(out.String(), "red") {
		t.Errorf("output = %q", out.String())
	}
	if len(gotBody.Messages) == 0 {
		t.Fatal("provider received no messages")
	}
	last := gotBody.Messages[len(gotBody.Messages)-1]
	parts, ok := last["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content = %#v, want text and image parts", last["content"])
	}
	text, _ := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "q" {
		t.Errorf("text part = %v", parts[0])
	}
	image, _ := parts[1].(map[string]any)
	u, _ := image["image_url"].(map[string]any)
	if s, _ := u["url"].(string); !strings.HasPrefix(s, "data:image/png;base64,") {
		t.Errorf("image part = %v", parts[1])
	}
}
