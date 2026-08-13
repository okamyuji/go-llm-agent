package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// defaultApprovalTimeout WithApprover で timeout 未指定 (0) のときの既定値
// 無期限待機 (apCtx = ctx の継承) は goroutine リークの原因になるため明示的なフォールバックを置く
const defaultApprovalTimeout = 5 * time.Minute

const (
	maxResponseRetries    = 3
	expansionAnswerPrefix = "直前の回答に含まれていない追加情報です。"
	untrustedInputSuffix  = "\n[END UNTRUSTED]"
)

// Run Strategy に処理を委譲する。Strategy 未設定なら ReAct で動く
func (s *service) Run(ctx context.Context, in Input, out chan<- Event) error {
	if s.strategy != nil {
		return s.strategy.run(ctx, s, in, out)
	}
	return s.runReAct(ctx, in, out)
}

// reactState runReAct が 1 ターンの間持ち回る可変状態。
// ループ本体と各段階の下位関数が同じ履歴・再試行カウンタを共有する
type reactState struct {
	msgs             []llm.Message
	turnMessageStart int
	// expansionRequested 直前の assistant 回答への追加説明依頼か
	expansionRequested        bool
	priorAssistantContent     string
	expansionGroundingContent string
	expansionRetries          int
	answerRetries             int
	// automaticWebToolsExecuted LLM の判断を待たず web_search / web_fetch を実行済みか
	automaticWebToolsExecuted bool
	validationRetries         int
	lastValidationCallID      string
	maxValidationRetries      int
}

// streamResult 1 回の LLM ストリーム呼び出しから取り出した結果
type streamResult struct {
	content string
	call    *llm.ToolCall
}

// streamAccumulator ストリームイベントの積算先
type streamAccumulator struct {
	content strings.Builder
	call    *llm.ToolCall
	usage   *llm.Usage
}

// emitFail EventError を送出して同じ err を返す。ターン打ち切り経路の共通形
func emitFail(out chan<- Event, err error) error {
	out <- Event{Kind: EventError, Err: err}
	return err
}

// runReAct LLM とツールを最大 MaxToolHops 回交互に呼ぶ ReAct スタイルのループ本体
func (s *service) runReAct(ctx context.Context, in Input, out chan<- Event) error {
	ctx, agentSpan := obs.StartAgentSpan(ctx, in.Model)
	defer agentSpan.End()

	prov, model, err := s.reg.Resolve(in.Model)
	if err != nil {
		return emitFail(out, err)
	}
	st, err := s.newReactState(ctx, in)
	if err != nil {
		return emitFail(out, err)
	}
	tools, tc, allowAutomaticWebTools := s.resolveToolPlan(in)
	if allowAutomaticWebTools && requiresWebSearch(st.msgs) {
		if werr := s.runAutomaticWebTools(ctx, in, st, out); werr != nil {
			return emitFail(out, werr)
		}
	}
	if st.automaticWebToolsExecuted {
		tc = nil
	}
	for hop := 0; hop <= in.MaxToolHops; hop++ {
		res, serr := s.streamTurn(ctx, prov, model, llm.ChatRequest{
			Model:       model,
			Messages:    st.msgs,
			Tools:       st.requestTools(tools, hop),
			ToolChoice:  tc,
			Temperature: generationRetryTemperature(max(st.expansionRetries, st.answerRetries)),
		}, in.SessionID, !st.expansionRequested && !st.automaticWebToolsExecuted, out)
		if serr != nil {
			return emitFail(out, serr)
		}
		if res.call == nil {
			content, retry := s.resolveAssistantContent(st, res.content, out)
			if retry {
				hop-- // 回答・追加説明の再生成は tool hop として数えない
				continue
			}
			final := llm.Message{Role: llm.RoleAssistant, Content: content}
			st.msgs = append(st.msgs, final)
			turnMessages := append([]llm.Message(nil), st.msgs[st.turnMessageStart:]...)
			out <- Event{Kind: EventFinal, Final: &final, TurnMessages: turnMessages}
			return nil
		}
		st.msgs = append(st.msgs, llm.Message{Role: llm.RoleAssistant, Content: res.content, ToolCalls: []llm.ToolCall{*res.call}})
		if terr := s.handleToolCall(ctx, in, st, res.call, out); terr != nil {
			return emitFail(out, terr)
		}
	}
	return emitFail(out, fmt.Errorf("max tool hops を超えました (%d)", in.MaxToolHops))
}

// newReactState system プロンプト付加・enricher 適用・入力スキャンを行い初期状態を組み立てる
func (s *service) newReactState(ctx context.Context, in Input) (*reactState, error) {
	msgs := append([]llm.Message{}, in.Messages...)
	if in.SystemPrompt != "" {
		msgs = append([]llm.Message{{Role: llm.RoleSystem, Content: in.SystemPrompt}}, msgs...)
	}
	if s.enricher != nil {
		enriched, enrichErr := s.enricher(ctx, msgs)
		if enrichErr != nil {
			slog.WarnContext(ctx, "context enricher failed, proceeding without enrichment", "err", enrichErr)
		} else {
			msgs = enriched
		}
	}
	if err := s.scanInput(ctx, msgs); err != nil {
		return nil, err
	}
	maxValidationRetries := max(in.ValidationMaxRetries, 0)
	if maxValidationRetries == 0 {
		maxValidationRetries = max(s.defaultMaxRetries, 0)
	}
	return &reactState{
		msgs:                      msgs,
		turnMessageStart:          len(msgs),
		expansionRequested:        requiresExpandedAnswer(msgs),
		priorAssistantContent:     latestPriorAssistantContent(msgs),
		expansionGroundingContent: latestToolResultContent(msgs, "web_fetch"),
		maxValidationRetries:      maxValidationRetries,
	}, nil
}

// scanInput 06 番設計書 入力スキャナを最初の LLM 呼び出し前に user/system メッセージへ適用する。
// クライアントには PatternID 等の detector 内部情報を返さない (検出ロジック露出を防ぐ)。
// 詳細なルール ID は slog でサーバ側にだけ残し、攻撃者へのフィードバックを最小化する
func (s *service) scanInput(ctx context.Context, msgs []llm.Message) error {
	if s.scanner == nil {
		return nil
	}
	for _, m := range msgs {
		if m.Role != llm.RoleUser && m.Role != llm.RoleSystem {
			continue
		}
		findings := s.scanner.Scan(m.Content)
		if len(findings) > 0 {
			slog.WarnContext(ctx, "input scanner blocked", "role", string(m.Role), "pattern_id", findings[0].PatternID)
			return fmt.Errorf("input blocked by safety scanner")
		}
	}
	return nil
}

// resolveToolPlan 送信するツール定義と tool_choice を決める。
// tool_choice none はツール定義の送信ごと抑制する。定義を送ったまま「呼ぶな」と
// 指示しても、tool_choice を無視するモデルがツール呼び出し JSON をテキストとして
// 出力する事故を防げないため。allowAutomaticWebTools は none 抑制より前の tc で判定する
func (s *service) resolveToolPlan(in Input) (tools []llm.ToolSpec, tc *llm.ToolChoice, allowAutomaticWebTools bool) {
	tc = in.ToolChoice
	if tc == nil {
		tc = s.defaultToolChoice
	}
	allowAutomaticWebTools = automaticToolChoice(tc)
	tools = s.specs()
	if tc != nil && tc.Mode == "none" {
		return nil, nil, allowAutomaticWebTools
	}
	return tools, tc, allowAutomaticWebTools
}

// requestTools 自動 Web 実行直後の最初の hop ではツール定義を送らない。
// 取得済みの情報での回答生成を優先させるため
func (st *reactState) requestTools(tools []llm.ToolSpec, hop int) []llm.ToolSpec {
	if st.automaticWebToolsExecuted && hop == 0 {
		return nil
	}
	return tools
}

// runAutomaticWebTools web_search と (成功時は) web_fetch を LLM の判断を待たずに実行し
// 結果を履歴へ積む。web_search 未登録なら何もしない
func (s *service) runAutomaticWebTools(ctx context.Context, in Input, st *reactState, out chan<- Event) error {
	if _, ok := s.tools.Lookup("web_search"); !ok {
		return nil
	}
	st.automaticWebToolsExecuted = true
	prompt := latestUserPrompt(st.msgs)
	searchArgs, err := json.Marshal(map[string]string{"query": prompt})
	if err != nil {
		return fmt.Errorf("web_search arguments: %w", err)
	}
	searchCall := llm.ToolCall{ID: nextAutomaticToolCallID(st.msgs, "web_search", "webs"), Name: "web_search", Arguments: searchArgs}
	outcome := s.invokeAutomaticTool(ctx, in.SessionID, st, searchCall, out)
	if outcome.IsError {
		return nil
	}
	return s.runAutomaticWebFetch(ctx, in, st, unwrapUntrusted(outcome.Content, "web_search"), prompt, out)
}

// runAutomaticWebFetch 検索結果から本文取得先を 1 件選び web_fetch を実行する
func (s *service) runAutomaticWebFetch(ctx context.Context, in Input, st *reactState, searchContent, prompt string, out chan<- Event) error {
	if _, ok := s.tools.Lookup("web_fetch"); !ok {
		return nil
	}
	fetchURL := selectWebFetchURL(searchContent, prompt)
	if fetchURL == "" {
		return nil
	}
	fetchArgs, err := json.Marshal(map[string]string{"url": fetchURL})
	if err != nil {
		return fmt.Errorf("web_fetch arguments: %w", err)
	}
	fetchCall := llm.ToolCall{ID: nextAutomaticToolCallID(st.msgs, "web_fetch", "webf"), Name: "web_fetch", Arguments: fetchArgs}
	s.invokeAutomaticTool(ctx, in.SessionID, st, fetchCall, out)
	return nil
}

// invokeAutomaticTool 自動実行するツール呼び出しを履歴へ積みつつ実行し結果を通知する
func (s *service) invokeAutomaticTool(ctx context.Context, sessionID string, st *reactState, call llm.ToolCall, out chan<- Event) ParallelOutcome {
	st.msgs = append(st.msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}})
	out <- Event{Kind: EventToolCall, ToolCall: &call}
	outcome := s.executeOne(ctx, sessionID, call)
	out <- Event{Kind: EventToolResult, ToolResult: &ToolResult{CallID: outcome.CallID, Name: outcome.Name, Content: outcome.Content, IsError: outcome.IsError}}
	st.msgs = append(st.msgs, llm.Message{Role: llm.RoleTool, Content: outcome.Content, ToolCallID: call.ID, Name: call.Name})
	return outcome
}

// streamTurn 1 回の LLM ストリーム呼び出しを行い、本文とツール呼び出しを取り出す。
// usage が届いた場合は EventUsage (と billing 集計) をここで済ませる
func (s *service) streamTurn(ctx context.Context, prov llm.Provider, model string, req llm.ChatRequest, sessionID string, emitDelta bool, out chan<- Event) (streamResult, error) {
	llmCtx, llmSpan := obs.StartLLMSpan(ctx, prov.Name(), model)
	defer llmSpan.End()
	stream, err := prov.Stream(llmCtx, req)
	if err != nil {
		return streamResult{}, err
	}
	var acc streamAccumulator
	for {
		ev, ok := stream.Recv()
		if !ok {
			break
		}
		if ev.Err != nil {
			if cerr := stream.Close(); cerr != nil {
				slog.WarnContext(ctx, "llm stream close failed after recv error",
					"provider", prov.Name(), "model", model, "err", cerr)
			}
			return streamResult{}, ev.Err
		}
		s.accumulateStreamEvent(&acc, ev, emitDelta, out)
	}
	if cerr := stream.Close(); cerr != nil {
		return streamResult{}, cerr
	}
	if acc.usage != nil {
		if uerr := s.emitUsage(ctx, llmCtx, sessionID, prov.Name(), model, acc.usage, out); uerr != nil {
			return streamResult{}, uerr
		}
	}
	return streamResult{content: acc.content.String(), call: acc.call}, nil
}

// accumulateStreamEvent 1 件のストリームイベントを積算し、必要なら中継イベントを送る
func (s *service) accumulateStreamEvent(acc *streamAccumulator, ev llm.StreamEvent, emitDelta bool, out chan<- Event) {
	if ev.DeltaText != "" {
		delta := ev.DeltaText
		if s.redactor != nil {
			delta = s.redactor.Redact(delta)
		}
		acc.content.WriteString(delta)
		if emitDelta {
			out <- Event{Kind: EventDelta, Delta: delta}
		}
	}
	if ev.ToolCall != nil {
		acc.call = ev.ToolCall
		out <- Event{Kind: EventToolCall, ToolCall: ev.ToolCall}
	}
	if ev.Usage != nil {
		acc.usage = ev.Usage
	}
}

// emitUsage 実測 usage を記録し EventUsage を送る。
// ErrBudgetExceeded を含むすべての billing エラーは呼び出し元へ伝播し、EventUsage は送らない
func (s *service) emitUsage(ctx, llmCtx context.Context, sessionID, provName, model string, usage *llm.Usage, out chan<- Event) error {
	obs.RecordTokens(llmCtx, provName, model, usage.InputTokens, usage.OutputTokens)
	ev := Event{Kind: EventUsage, Usage: usage}
	if s.billing != nil {
		if sessionID == "" {
			sessionID = "default"
		}
		snap, berr := s.billing.Add(ctx, sessionID, provName, model, usage.InputTokens, usage.OutputTokens)
		if berr != nil {
			return berr
		}
		snapCopy := snap
		ev.Cost = &snapCopy
	}
	out <- ev
	return nil
}

// resolveAssistantContent ツール呼び出しを伴わない応答の後処理を行う。
// 自動 Web 実行後と追加説明依頼では内容が不十分なら再生成を要求する (retry=true)
func (s *service) resolveAssistantContent(st *reactState, content string, out chan<- Event) (string, bool) {
	if st.automaticWebToolsExecuted {
		if !answerContentSufficient(content) && st.answerRetries < maxResponseRetries {
			st.answerRetries++
			return content, true
		}
		if !answerContentSufficient(content) {
			content = "Web取得結果から完全な回答を生成できませんでした。質問を具体化して再実行してください。"
		}
		out <- Event{Kind: EventDelta, Delta: content}
	}
	if st.expansionRequested {
		expanded := expandedAnswerContent(st.priorAssistantContent, st.expansionGroundingContent, content)
		if !expansionAnswerSufficient(expanded) && st.expansionRetries < maxResponseRetries {
			st.expansionRetries++
			return content, true
		}
		if !expansionAnswerSufficient(expanded) {
			expanded = "直前の回答と重複しない追加情報を生成できませんでした。質問の観点を指定してください。"
		}
		content = expanded
		out <- Event{Kind: EventDelta, Delta: content}
	}
	return content, false
}

// handleToolCall 1 件の ToolCall を検証・承認・実行し結果を履歴へ積む。
// 非 nil の戻り値はターンの打ち切りを意味する。nil はスキップ・実行のいずれでも
// 次の hop へ進んでよいことを表す
func (s *service) handleToolCall(ctx context.Context, in Input, st *reactState, call *llm.ToolCall, out chan<- Event) error {
	t, ok := s.tools.Lookup(call.Name)
	if !ok {
		return fmt.Errorf("tool %q が見つかりません", call.Name)
	}
	proceed, verr := s.validateToolArgs(st, call, out)
	if verr != nil || !proceed {
		return verr
	}
	approved, aerr := s.requestApproval(ctx, in, st, call, out)
	if aerr != nil || !approved {
		return aerr
	}
	s.executeAndRecord(ctx, st, t, call, out)
	return nil
}

// validateToolArgs JSON Schema 検証を行う。検証失敗は budget の範囲で
// モデルへ訂正を促すツール結果を積み (proceed=false, err=nil)、budget 超過は err を返す
func (s *service) validateToolArgs(st *reactState, call *llm.ToolCall, out chan<- Event) (bool, error) {
	if s.validator == nil {
		return true, nil
	}
	// 異なる ToolCall に切り替わった場合は budget を per-call で初期化する
	if call.ID != st.lastValidationCallID {
		st.validationRetries = 0
		st.lastValidationCallID = call.ID
	}
	vok, vmsg := s.validator.Validate(call.Name, call.Arguments)
	if vok {
		return true, nil
	}
	if st.validationRetries < st.maxValidationRetries {
		st.validationRetries++
		st.appendToolError(out, call, "schema validation failed: "+vmsg+" — please correct the arguments to match the JSON schema and try again")
		return false, nil
	}
	if st.maxValidationRetries == 0 {
		return false, fmt.Errorf("schema validation failed (retries disabled): %s", vmsg)
	}
	return false, fmt.Errorf("schema validation max retries (%d) exceeded: %s", st.maxValidationRetries, vmsg)
}

// requestApproval 承認が必要なツールについて承認を待つ。
// 拒否は (false, nil) で、ツール結果へ拒否として積みターンは継続する
func (s *service) requestApproval(ctx context.Context, in Input, st *reactState, call *llm.ToolCall, out chan<- Event) (bool, error) {
	if s.approver == nil || !s.approvalRequired[call.Name] {
		return true, nil
	}
	runID := in.SessionID
	if runID == "" {
		runID = "default"
	}
	// approvalTimeout 未指定 (0) は無期限待機による goroutine leak を招くため
	// defaultApprovalTimeout (5 分) にフォールバックする
	timeout := s.approvalTimeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	apCtx, apCancel := context.WithTimeout(ctx, timeout)
	d, aerr := s.approver.Request(apCtx, ApprovalRequest{
		RunID: runID, CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments,
	})
	apCancel()
	if aerr != nil && !errors.Is(aerr, ErrApprovalTimeout) {
		return false, aerr
	}
	if !d.Allowed {
		st.appendToolError(out, call, "tool execution denied by reviewer: "+d.Reason)
		return false, nil
	}
	return true, nil
}

// executeAndRecord ツールを実行し、結果を通知して履歴へ積む
func (s *service) executeAndRecord(ctx context.Context, st *reactState, t tool.Tool, call *llm.ToolCall, out chan<- Event) {
	execCtx := context.WithValue(ctx, tool.CorrelationKey(), call.ID)
	execCtx, toolSpan := obs.StartToolSpan(execCtx, call.Name, call.ID)
	start := time.Now()
	res, terr := t.Execute(execCtx, call.Arguments)
	ok := terr == nil && !res.IsError
	obs.RecordToolOutcome(execCtx, call.Name, ok, time.Since(start))
	toolSpan.End()
	content := res.Content
	if terr != nil {
		content = terr.Error()
	}
	// 06 番設計書 全ツール出力に untrusted マーカーを無条件付与する
	// 旧実装は "[UNTRUSTED INPUT" 接頭辞ありで skip していたが、ツールが攻撃的に
	// 自前マーカーを偽装した場合に外周のラップを回避できる穴があったため常に付与する
	content = wrapUntrusted(content, call.Name)
	if s.redactor != nil {
		content = s.redactor.Redact(content)
	}
	tr := &ToolResult{CallID: call.ID, Name: call.Name, Content: content, IsError: !ok}
	out <- Event{Kind: EventToolResult, ToolResult: tr}
	// 02 番設計書 履歴へ積むツール結果だけを上限文字数まで切り詰める。
	// EventToolResult で通知済みの tr.Content (全文) は変更しない。
	st.msgs = append(st.msgs, llm.Message{Role: llm.RoleTool, Content: TruncateToolResult(tr.Content, s.toolResultLimitMaxChars), ToolCallID: call.ID, Name: call.Name})
}

// appendToolError 拒否・検証失敗をツール結果として通知し履歴へ積む
func (st *reactState) appendToolError(out chan<- Event, call *llm.ToolCall, content string) {
	tr := &ToolResult{CallID: call.ID, Name: call.Name, Content: content, IsError: true}
	out <- Event{Kind: EventToolResult, ToolResult: tr}
	st.msgs = append(st.msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: call.ID, Name: call.Name})
}

func automaticToolChoice(tc *llm.ToolChoice) bool {
	return tc == nil || tc.Mode == "" || tc.Mode == "auto"
}

// requiresWebSearch は最新のuserメッセージが外部の現在情報またはWeb検索を求めるか判定する。
// 判定はモデルに依存させず、Webツールがopt-inされた構成でだけ呼び出し側が利用する。
func requiresWebSearch(msgs []llm.Message) bool {
	prompt := strings.ToLower(latestUserPrompt(msgs))
	if prompt == "" {
		return false
	}
	for _, term := range []string{
		"最新", "ニュース", "天気",
		"search the web", "look up",
	} {
		if strings.Contains(prompt, term) {
			return true
		}
	}
	words := wordSet(prompt)
	for _, term := range []string{"browse", "latest", "current", "today", "news", "weather"} {
		if _, ok := words[term]; ok {
			return true
		}
	}
	hasWeb := strings.Contains(prompt, "web") || strings.Contains(prompt, "ウェブ") || strings.Contains(prompt, "ネット")
	hasSearch := strings.Contains(prompt, "検索") || strings.Contains(prompt, "調査") || strings.Contains(prompt, "search") || strings.Contains(prompt, "research")
	return hasWeb && hasSearch
}

func latestUserPrompt(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

func latestPriorAssistantContent(msgs []llm.Message) string {
	latestUser := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			latestUser = i
			break
		}
	}
	for i := latestUser - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func latestToolResultContent(msgs []llm.Message, toolName string) string {
	latestUser := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			latestUser = i
			break
		}
	}
	for i := latestUser - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleTool && msgs[i].Name == toolName {
			return msgs[i].Content
		}
	}
	return ""
}

// expandedAnswerContent は生成候補から直前回答に含まれる部分を除き、追加情報だけを返す。
func expandedAnswerContent(prior, grounding, candidate string) string {
	priorNormalized := normalizeExpansionText(prior)
	priorDigits := digitSequence(prior)
	groundingNormalized := normalizeExpansionText(grounding)
	requireJapanese := containsJapaneseKana(prior)
	var novel []string
	for _, segment := range splitExpansionSegments(candidate) {
		segment = strings.TrimSpace(strings.TrimLeft(segment, "#*+-_>• \t"))
		normalized := normalizeExpansionText(segment)
		if len([]rune(normalized)) < 4 ||
			(requireJapanese && !containsJapaneseKana(segment)) ||
			expansionFiller(normalized) ||
			expansionTemplateArtifact(segment) ||
			expansionCoveredByPrior(priorNormalized, normalized) {
			continue
		}
		if digits := digitSequence(segment); len(digits) >= 2 && strings.Contains(priorDigits, digits) {
			continue
		}
		if groundingNormalized != "" && !expansionGroundedByTool(groundingNormalized, normalized) {
			continue
		}
		novel = append(novel, segment)
		if len(novel) == 5 {
			break
		}
	}
	if len(novel) == 0 {
		return ""
	}
	return expansionAnswerPrefix + "\n\n- " + strings.Join(novel, "\n- ")
}

// containsJapaneseKana は日本語回答に通常含まれるひらがな・カタカナの有無を返す。
func containsJapaneseKana(content string) bool {
	for _, r := range content {
		if (r >= '\u3040' && r <= '\u30ff') || (r >= '\uff66' && r <= '\uff9f') {
			return true
		}
	}
	return false
}

// splitExpansionSegments は改行で候補を分割しつつ、文末記号を各文に残す。
func splitExpansionSegments(candidate string) []string {
	var segments []string
	var current strings.Builder
	for _, r := range candidate {
		if r == '\n' {
			if current.Len() > 0 {
				segments = append(segments, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
		if r == '。' || r == '！' || r == '？' || r == '!' || r == '?' {
			segments = append(segments, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

// generationRetryTemperature は再試行ごとに生成条件を変え、同一要求の反復を避ける。
func generationRetryTemperature(retry int) *float64 {
	if retry <= 0 {
		return nil
	}
	temperature := min(float64(retry*3)/10, 0.9)
	return &temperature
}

// answerContentSufficient は自動Web実行後の出力が空文・見出し・生のツール要求でないことを確認する。
func answerContentSufficient(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || expansionFiller(normalizeExpansionText(trimmed)) {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "[tool_calls]") ||
		(strings.Contains(lower, `"name"`) && strings.Contains(lower, `"arguments"`) && strings.Contains(lower, "web_")) {
		return false
	}
	return true
}

// wrapUntrusted はツール出力をモデル向けの非信頼入力境界で囲む。
func wrapUntrusted(content, toolName string) string {
	return "[UNTRUSTED INPUT: tool=" + toolName + "]\n" + content + untrustedInputSuffix
}

// unwrapUntrusted は指定ツールの外側の非信頼入力境界だけを取り除く。
func unwrapUntrusted(content, toolName string) string {
	prefix := "[UNTRUSTED INPUT: tool=" + toolName + "]\n"
	return strings.TrimSuffix(strings.TrimPrefix(content, prefix), untrustedInputSuffix)
}

func digitSequence(s string) string {
	var digits strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits.WriteRune(r)
		}
	}
	return digits.String()
}

func expansionGroundedByTool(grounding, candidate string) bool {
	candidateRunes := []rune(candidate)
	groundingRunes := []rune(grounding)
	if len(candidateRunes) < 6 || len(groundingRunes) < 3 {
		return false
	}
	for i := 0; i <= len(candidateRunes)-6; i++ {
		if strings.Contains(grounding, string(candidateRunes[i:i+6])) {
			return true
		}
	}
	groundingTrigrams := make(map[string]struct{}, len(groundingRunes)-2)
	for i := 0; i <= len(groundingRunes)-3; i++ {
		groundingTrigrams[string(groundingRunes[i:i+3])] = struct{}{}
	}
	matched := 0
	total := len(candidateRunes) - 2
	for i := 0; i <= len(candidateRunes)-3; i++ {
		if _, ok := groundingTrigrams[string(candidateRunes[i:i+3])]; ok {
			matched++
		}
	}
	return matched*100 >= total*30
}

func expansionAnswerSufficient(content string) bool {
	if content == "" {
		return false
	}
	return strings.Count(content, "\n- ") >= 2
}

func expansionFiller(normalized string) bool {
	switch normalized {
	case "以下の通り", "以下の通りです", "詳しく説明します", "追加情報", "追加情報です", "参考情報", "参考情報です":
		return true
	default:
		return false
	}
}

func expansionTemplateArtifact(segment string) bool {
	identifierLength := 0
	for _, r := range segment {
		switch {
		case (r >= 'A' && r <= 'Z') || r == '_':
			identifierLength++
		case r == '=' && identifierLength >= 3:
			return true
		default:
			identifierLength = 0
		}
	}
	return false
}

func expansionCoveredByPrior(prior, candidate string) bool {
	if strings.Contains(prior, candidate) {
		return true
	}
	candidateRunes := []rune(candidate)
	priorRunes := []rune(prior)
	if len(candidateRunes) < 8 || len(priorRunes) < 3 {
		return false
	}
	for i := 0; i <= len(candidateRunes)-8; i++ {
		if strings.Contains(prior, string(candidateRunes[i:i+8])) {
			return true
		}
	}
	priorTrigrams := make(map[string]struct{}, len(priorRunes)-2)
	for i := 0; i <= len(priorRunes)-3; i++ {
		priorTrigrams[string(priorRunes[i:i+3])] = struct{}{}
	}
	matched := 0
	total := len(candidateRunes) - 2
	for i := 0; i <= len(candidateRunes)-3; i++ {
		if _, ok := priorTrigrams[string(candidateRunes[i:i+3])]; ok {
			matched++
		}
	}
	return matched*100 >= total*80
}

func normalizeExpansionText(s string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

// nextAutomaticToolCallID は同じ会話履歴で重複しない9文字英数字のIDを生成する。
func nextAutomaticToolCallID(msgs []llm.Message, toolName, prefix string) string {
	sequence := 1
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls {
			if call.Name == toolName {
				sequence++
			}
		}
	}
	return fmt.Sprintf("%s%05d", prefix, sequence)
}

// maxExpansionPromptRunes 追加説明依頼とみなす user メッセージの最大文字数。
// 「もう少し詳しく。」のような照応的な依頼は短く、新規の質問は話題を含むため長くなる。
// 誤って新規質問を追加説明扱いすると回答が前ターンの内容でフィルタされ失われるため、
// 取りこぼし（通常経路で回答される）より誤爆を避ける側に倒して短めに取る。
const maxExpansionPromptRunes = 20

// requiresExpandedAnswer は前のassistant回答に対する追加説明の依頼か判定する。
// 新しい話題を含む長文質問を誤判定しないよう、短い依頼だけを対象にする。
func requiresExpandedAnswer(msgs []llm.Message) bool {
	latestUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			latestUser = i
			break
		}
	}
	if latestUser < 0 {
		return false
	}
	hasPriorAssistant := false
	for i := 0; i < latestUser; i++ {
		if msgs[i].Role == llm.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			hasPriorAssistant = true
			break
		}
	}
	if !hasPriorAssistant {
		return false
	}
	prompt := strings.ToLower(strings.TrimSpace(msgs[latestUser].Content))
	if len([]rune(prompt)) > maxExpansionPromptRunes {
		return false
	}
	for _, phrase := range []string{"もう少し詳しく", "詳しく", "詳細", "more detail"} {
		if strings.Contains(prompt, phrase) {
			return true
		}
	}
	words := wordSet(prompt)
	for _, word := range []string{"elaborate", "expand"} {
		if _, ok := words[word]; ok {
			return true
		}
	}
	return false
}

// selectWebFetchURL は検索結果から本文取得に使う有効なHTTP(S) URLを1件選ぶ。
// 「公式」指定時はuserメッセージ中の英数字語とhost名の区切り語が一致するURLを優先する。
func selectWebFetchURL(searchContent, prompt string) string {
	var payload struct {
		Results []struct {
			URL string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(searchContent), &payload); err != nil {
		return ""
	}
	officialRequested := strings.Contains(prompt, "公式") || strings.Contains(strings.ToLower(prompt), "official")
	promptWords := wordSet(prompt)
	bestURL := ""
	bestScore := -1
	for _, result := range payload.Results {
		u, err := url.Parse(result.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			continue
		}
		score := 0
		if officialRequested {
			for word := range wordSet(u.Hostname()) {
				if len(word) < 2 || word == "www" {
					continue
				}
				if _, ok := promptWords[word]; ok {
					score++
				}
			}
		}
		if bestURL == "" || score > bestScore {
			bestURL = result.URL
			bestScore = score
		}
	}
	return bestURL
}

func wordSet(s string) map[string]struct{} {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}

func (s *service) specs() []llm.ToolSpec {
	var out []llm.ToolSpec
	for _, sp := range s.tools.List() {
		out = append(out, llm.ToolSpec{Name: sp.Name, Description: sp.Description, Schema: sp.Schema})
	}
	return out
}
