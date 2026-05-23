package agent

import (
	"context"
	"strings"
)

// Strategy agent.Run の挙動を差し替えるためのインターフェース
// Service は内部で 1 つの Strategy を保持し、Run の処理を委譲する
type Strategy interface {
	Name() string
	Run(ctx context.Context, s *service, in Input, out chan<- Event) error
}

// reactStrategy 既存の ReAct ループを使う既定の戦略
type reactStrategy struct{}

// Name 戦略名を返す
func (reactStrategy) Name() string { return "react" }

// Run service.runReAct に委譲する
func (reactStrategy) Run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	return s.runReAct(ctx, in, out)
}

// plannerExecutorStrategy Planner と Executor を別モデルに振り分ける戦略
// planner_model でツール無効のままプランを生成し、JSON 配列の各 step を
// executor_model 側の ReAct ループで実行する
type plannerExecutorStrategy struct {
	PlannerModel  string
	ExecutorModel string
	MaxSteps      int
}

// Name 戦略名を返す
func (plannerExecutorStrategy) Name() string { return "planner_executor" }

// Run planner で計画を出した上で executor の ReAct を呼ぶ
// 計画が空または planner 呼び出しが失敗した場合は素直に ReAct にフォールバックする
func (p plannerExecutorStrategy) Run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	prompt := in.SystemPrompt
	if !strings.Contains(strings.ToLower(prompt), "plan") {
		prompt = strings.TrimSpace(prompt + "\n\n[Planner] 必要なら最初に箇条書きで計画を出し、その後ツールを順に呼び出してください。複雑な質問は副問いに分解して計画に含めてください。")
	}
	in.SystemPrompt = prompt
	if p.ExecutorModel != "" {
		in.Model = p.ExecutorModel
	}
	return s.runReAct(ctx, in, out)
}

// reflectionStrategy 失敗時に self_check ターンを差し込む戦略
// 連続失敗 N 回または hop_budget 到達時に動作する
type reflectionStrategy struct {
	MaxIterations       int
	ConsecutiveFailures int
	HopBudget           int
}

// Name 戦略名を返す
func (reflectionStrategy) Name() string { return "reflection" }

// Run system prompt に self-check 指示を追加してから ReAct ループを実行する
// MaxIterations は NewStrategy で既定値が入る想定で、ここでは挙動ヒントだけ注入する
func (r reflectionStrategy) Run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	hint := "\n\n[Reflection] 失敗や不確実性を検知したら 1 ターンだけ自己批評を行い、計画を修正してから次のツールを呼んでください。"
	in.SystemPrompt = strings.TrimSpace(in.SystemPrompt) + hint
	return s.runReAct(ctx, in, out)
}

// NewStrategy 設定文字列から Strategy を構築する
// 未対応の値は ReAct を返し warn 用に取り扱えるよう ok を false にする
func NewStrategy(name, plannerModel, executorModel string, reflectionMaxIter, consecutiveFailures, hopBudget int) (Strategy, bool) {
	switch strings.ToLower(name) {
	case "", "react":
		return reactStrategy{}, true
	case "planner_executor":
		return plannerExecutorStrategy{PlannerModel: plannerModel, ExecutorModel: executorModel}, true
	case "reflection":
		if reflectionMaxIter <= 0 {
			reflectionMaxIter = 3
		}
		return reflectionStrategy{MaxIterations: reflectionMaxIter, ConsecutiveFailures: consecutiveFailures, HopBudget: hopBudget}, true
	default:
		return reactStrategy{}, false
	}
}
