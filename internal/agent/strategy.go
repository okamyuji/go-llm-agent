package agent

import (
	"context"
	"strings"
)

// Strategy agent.Run の挙動を差し替えるためのインターフェース
// 公開 API として WithStrategy / NewStrategy 経由で利用される
// 実装はパッケージ内に限定する。非公開メソッド run() を持たせて外部実装を不能にし、
// 内部型 *service を露出するシグネチャ汚染を避ける
type Strategy interface {
	Name() string
	run(ctx context.Context, s *service, in Input, out chan<- Event) error
}

// reactStrategy 既存の ReAct ループを使う既定の戦略
type reactStrategy struct{}

// Name 戦略名を返す
func (reactStrategy) Name() string { return "react" }

// run service.runReAct に委譲する
func (reactStrategy) run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	return s.runReAct(ctx, in, out)
}

// plannerExecutorStrategy planner_executor の MVP 実装
// 本格的な「planner LLM を別呼び出しして JSON プランを生成し executor が逐次実行する」設計は
// 将来フェーズで実装する。現状は system prompt に [Planner] ヒントを注入し、
// executor_model が指定されていれば Input.Model を上書きして単一の ReAct ループに委ねる
// PlannerModel と MaxSteps は将来の本実装で参照するため設定として受け取るが、
// 現在の run() からは ExecutorModel のみを利用する
type plannerExecutorStrategy struct {
	PlannerModel  string
	ExecutorModel string
	MaxSteps      int
}

// Name 戦略名を返す
func (plannerExecutorStrategy) Name() string { return "planner_executor" }

// run system prompt に [Planner] ヒントを注入してから ReAct を 1 回実行する
// executor_model が設定されていれば Input.Model を上書きする
func (p plannerExecutorStrategy) run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	prompt := in.SystemPrompt
	// "[Planner]" タグの有無で既存のプランナー指示を判定する
	// "plan" 単語マッチは "explain"/"complaint" などに誤マッチするため避ける
	// 大小文字どちらでも検出できるよう ToLower で比較する (元のケーシングは保持する)
	if !strings.Contains(strings.ToLower(prompt), "[planner]") {
		prompt = strings.TrimSpace(prompt + "\n\n[Planner] 必要なら最初に箇条書きで計画を出し、その後ツールを順に呼び出してください。複雑な質問は副問いに分解して計画に含めてください。")
	}
	in.SystemPrompt = prompt
	if p.ExecutorModel != "" {
		in.Model = p.ExecutorModel
	}
	return s.runReAct(ctx, in, out)
}

// reflectionStrategy 失敗時に self_check ターンを差し込む戦略の MVP
// MaxIterations / ConsecutiveFailures / HopBudget はループ制御用パラメータとして
// 将来 self-critique ループを追加する際に参照する。現在の run() ではプロンプトヒント
// 注入のみで、上記フィールドは未参照のままにしてある
type reflectionStrategy struct {
	MaxIterations       int
	ConsecutiveFailures int
	HopBudget           int
}

// Name 戦略名を返す
func (reflectionStrategy) Name() string { return "reflection" }

// run system prompt に self-check 指示を追加してから ReAct ループを実行する
// 完全な reflection ループ (連続失敗時に自己批評を強制) は将来フェーズで実装する
func (r reflectionStrategy) run(ctx context.Context, s *service, in Input, out chan<- Event) error {
	hint := "\n\n[Reflection] 失敗や不確実性を検知したら 1 ターンだけ自己批評を行い、計画を修正してから次のツールを呼んでください。"
	in.SystemPrompt = strings.TrimSpace(in.SystemPrompt) + hint
	return s.runReAct(ctx, in, out)
}

// NewStrategy 設定文字列から Strategy を構築する
// 未対応の値は ReAct を返し warn 用に取り扱えるよう ok を false にする
// plannerExecutor の MaxSteps と reflection の MaxIterations / ConsecutiveFailures / HopBudget は
// 将来の本実装フェーズで参照される構成値で、本 MVP では struct に保持するのみ
func NewStrategy(name, plannerModel, executorModel string, plannerMaxSteps, reflectionMaxIter, consecutiveFailures, hopBudget int) (Strategy, bool) {
	switch strings.ToLower(name) {
	case "", "react":
		return reactStrategy{}, true
	case "planner_executor":
		return plannerExecutorStrategy{
			PlannerModel:  plannerModel,
			ExecutorModel: executorModel,
			MaxSteps:      plannerMaxSteps,
		}, true
	case "reflection":
		if reflectionMaxIter <= 0 {
			reflectionMaxIter = 3
		}
		return reflectionStrategy{MaxIterations: reflectionMaxIter, ConsecutiveFailures: consecutiveFailures, HopBudget: hopBudget}, true
	default:
		return reactStrategy{}, false
	}
}
