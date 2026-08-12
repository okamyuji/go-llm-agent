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

// runReAct LLM とツールを最大 MaxToolHops 回交互に呼ぶ ReAct スタイルのループ本体
func (s *service) runReAct(ctx context.Context, in Input, out chan<- Event) error {
	ctx, agentSpan := obs.StartAgentSpan(ctx, in.Model)
	defer agentSpan.End()

	prov, model, err := s.reg.Resolve(in.Model)
	if err != nil {
		out <- Event{Kind: EventError, Err: err}
		return err
	}
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
	expansionRequested := requiresExpandedAnswer(msgs)
	priorAssistantContent := latestPriorAssistantContent(msgs)
	expansionGroundingContent := latestToolResultContent(msgs, "web_fetch")
	turnMessageStart := len(msgs)
	// 06 番設計書 入力スキャナを最初の LLM 呼び出し前にすべての user/system メッセージへ適用する
	// 検出された場合は EventError で早期リターンする (fail-closed)
	// クライアントには PatternID 等の detector 内部情報を返さない (検出ロジック露出を防ぐ)
	// 詳細なルール ID は slog でサーバ側にだけ残し、攻撃者へのフィードバックを最小化する
	if s.scanner != nil {
		for _, m := range msgs {
			if m.Role != llm.RoleUser && m.Role != llm.RoleSystem {
				continue
			}
			findings := s.scanner.Scan(m.Content)
			if len(findings) > 0 {
				slog.WarnContext(ctx, "input scanner blocked", "role", string(m.Role), "pattern_id", findings[0].PatternID)
				err := fmt.Errorf("input blocked by safety scanner")
				out <- Event{Kind: EventError, Err: err}
				return err
			}
		}
	}
	validationRetries := 0
	lastValidationCallID := ""
	maxValidationRetries := max(in.ValidationMaxRetries, 0)
	if maxValidationRetries == 0 {
		maxValidationRetries = max(s.defaultMaxRetries, 0)
	}
	tc := in.ToolChoice
	if tc == nil {
		tc = s.defaultToolChoice
	}
	allowAutomaticWebTools := automaticToolChoice(tc)
	// tool_choice none はツール定義の広告ごと抑制する。
	// 定義を広告したまま「呼ぶな」と指示しても、tool_choice を無視するモデルが
	// ツール呼び出し JSON をテキストとして出力する事故を防げないため
	tools := s.specs()
	if tc != nil && tc.Mode == "none" {
		tools = nil
		tc = nil
	}
	automaticWebToolsExecuted := false
	if allowAutomaticWebTools && requiresWebSearch(msgs) {
		if _, ok := s.tools.Lookup("web_search"); ok {
			automaticWebToolsExecuted = true
			prompt := latestUserPrompt(msgs)
			searchArgs, marshalErr := json.Marshal(map[string]string{"query": prompt})
			if marshalErr != nil {
				err := fmt.Errorf("web_search arguments: %w", marshalErr)
				out <- Event{Kind: EventError, Err: err}
				return err
			}
			searchCall := llm.ToolCall{ID: nextAutomaticToolCallID(msgs, "web_search", "webs"), Name: "web_search", Arguments: searchArgs}
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{searchCall}})
			out <- Event{Kind: EventToolCall, ToolCall: &searchCall}
			searchOutcome := s.executeOne(ctx, in.SessionID, searchCall)
			searchResult := &ToolResult{CallID: searchOutcome.CallID, Name: searchOutcome.Name, Content: searchOutcome.Content, IsError: searchOutcome.IsError}
			out <- Event{Kind: EventToolResult, ToolResult: searchResult}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: searchOutcome.Content, ToolCallID: searchCall.ID, Name: searchCall.Name})

			if !searchOutcome.IsError {
				if _, ok := s.tools.Lookup("web_fetch"); ok {
					searchContent := unwrapUntrusted(searchOutcome.Content, "web_search")
					if fetchURL := selectWebFetchURL(searchContent, prompt); fetchURL != "" {
						fetchArgs, fetchMarshalErr := json.Marshal(map[string]string{"url": fetchURL})
						if fetchMarshalErr != nil {
							err := fmt.Errorf("web_fetch arguments: %w", fetchMarshalErr)
							out <- Event{Kind: EventError, Err: err}
							return err
						}
						fetchCall := llm.ToolCall{ID: nextAutomaticToolCallID(msgs, "web_fetch", "webf"), Name: "web_fetch", Arguments: fetchArgs}
						msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{fetchCall}})
						out <- Event{Kind: EventToolCall, ToolCall: &fetchCall}
						fetchOutcome := s.executeOne(ctx, in.SessionID, fetchCall)
						fetchResult := &ToolResult{CallID: fetchOutcome.CallID, Name: fetchOutcome.Name, Content: fetchOutcome.Content, IsError: fetchOutcome.IsError}
						out <- Event{Kind: EventToolResult, ToolResult: fetchResult}
						msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: fetchOutcome.Content, ToolCallID: fetchCall.ID, Name: fetchCall.Name})
					}
				}
			}
		}
	}
	if automaticWebToolsExecuted {
		tc = nil
	}
	expansionRetries := 0
	answerRetries := 0
	for hop := 0; hop <= in.MaxToolHops; hop++ {
		requestTools := tools
		if automaticWebToolsExecuted && hop == 0 {
			requestTools = nil
		}
		llmCtx, llmSpan := obs.StartLLMSpan(ctx, prov.Name(), model)
		stream, err := prov.Stream(llmCtx, llm.ChatRequest{
			Model:       model,
			Messages:    msgs,
			Tools:       requestTools,
			ToolChoice:  tc,
			Temperature: generationRetryTemperature(max(expansionRetries, answerRetries)),
		})
		if err != nil {
			llmSpan.End()
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		var contentBuilder strings.Builder
		var pendingCall *llm.ToolCall
		var lastUsage *llm.Usage
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
				llmSpan.End()
				out <- Event{Kind: EventError, Err: ev.Err}
				return ev.Err
			}
			if ev.DeltaText != "" {
				delta := ev.DeltaText
				if s.redactor != nil {
					delta = s.redactor.Redact(delta)
				}
				contentBuilder.WriteString(delta)
				if !expansionRequested && !automaticWebToolsExecuted {
					out <- Event{Kind: EventDelta, Delta: delta}
				}
			}
			if ev.ToolCall != nil {
				pendingCall = ev.ToolCall
				out <- Event{Kind: EventToolCall, ToolCall: ev.ToolCall}
			}
			if ev.Usage != nil {
				lastUsage = ev.Usage
			}
		}
		assistantContent := contentBuilder.String()
		if err := stream.Close(); err != nil {
			llmSpan.End()
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		if lastUsage != nil {
			obs.RecordTokens(llmCtx, prov.Name(), model, lastUsage.InputTokens, lastUsage.OutputTokens)
			ev := Event{Kind: EventUsage, Usage: lastUsage}
			if s.billing != nil {
				sessionID := in.SessionID
				if sessionID == "" {
					sessionID = "default"
				}
				snap, berr := s.billing.Add(ctx, sessionID, prov.Name(), model, lastUsage.InputTokens, lastUsage.OutputTokens)
				if berr != nil {
					// ErrBudgetExceeded を含むすべての billing エラーは EventError として伝播する
					// 旧コードは ErrBudgetExceeded 分岐で同一処理を二重に書いていたため統合した
					llmSpan.End()
					out <- Event{Kind: EventError, Err: berr}
					return berr
				}
				snapCopy := snap
				ev.Cost = &snapCopy
			}
			out <- ev
		}
		llmSpan.End()

		if pendingCall == nil && automaticWebToolsExecuted {
			if !answerContentSufficient(assistantContent) && answerRetries < maxResponseRetries {
				answerRetries++
				hop-- // 回答の再生成はtool hopとして数えない。
				continue
			}
			if !answerContentSufficient(assistantContent) {
				assistantContent = "Web取得結果から完全な回答を生成できませんでした。質問を具体化して再実行してください。"
			}
			out <- Event{Kind: EventDelta, Delta: assistantContent}
		}

		if pendingCall == nil && expansionRequested {
			expandedContent := expandedAnswerContent(priorAssistantContent, expansionGroundingContent, assistantContent)
			if !expansionAnswerSufficient(expandedContent) && expansionRetries < maxResponseRetries {
				expansionRetries++
				hop-- // 追加説明の再生成はtool hopとして数えない。
				continue
			}
			if !expansionAnswerSufficient(expandedContent) {
				expandedContent = "直前の回答と重複しない追加情報を生成できませんでした。質問の観点を指定してください。"
			}
			assistantContent = expandedContent
			out <- Event{Kind: EventDelta, Delta: assistantContent}
		}

		assistant := llm.Message{Role: llm.RoleAssistant, Content: assistantContent}
		if pendingCall != nil {
			assistant.ToolCalls = []llm.ToolCall{*pendingCall}
		}
		msgs = append(msgs, assistant)

		if pendingCall == nil {
			final := assistant
			turnMessages := append([]llm.Message(nil), msgs[turnMessageStart:]...)
			out <- Event{Kind: EventFinal, Final: &final, TurnMessages: turnMessages}
			return nil
		}

		t, ok := s.tools.Lookup(pendingCall.Name)
		if !ok {
			err := fmt.Errorf("tool %q が見つかりません", pendingCall.Name)
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		if s.validator != nil {
			// 異なる ToolCall に切り替わった場合は budget を per-call で初期化する
			if pendingCall.ID != lastValidationCallID {
				validationRetries = 0
				lastValidationCallID = pendingCall.ID
			}
			if vok, vmsg := s.validator.Validate(pendingCall.Name, pendingCall.Arguments); !vok {
				if validationRetries < maxValidationRetries {
					validationRetries++
					tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: "schema validation failed: " + vmsg + " — please correct the arguments to match the JSON schema and try again", IsError: true}
					out <- Event{Kind: EventToolResult, ToolResult: tr}
					msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
					continue
				}
				var err error
				if maxValidationRetries == 0 {
					err = fmt.Errorf("schema validation failed (retries disabled): %s", vmsg)
				} else {
					err = fmt.Errorf("schema validation max retries (%d) exceeded: %s", maxValidationRetries, vmsg)
				}
				out <- Event{Kind: EventError, Err: err}
				return err
			}
		}
		if s.approver != nil && s.approvalRequired[pendingCall.Name] {
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
				RunID: runID, CallID: pendingCall.ID, ToolName: pendingCall.Name, Arguments: pendingCall.Arguments,
			})
			apCancel()
			if aerr != nil && !errors.Is(aerr, ErrApprovalTimeout) {
				out <- Event{Kind: EventError, Err: aerr}
				return aerr
			}
			if !d.Allowed {
				tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: "tool execution denied by reviewer: " + d.Reason, IsError: true}
				out <- Event{Kind: EventToolResult, ToolResult: tr}
				msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
				continue
			}
		}
		execCtx := context.WithValue(ctx, tool.CorrelationKey(), pendingCall.ID)
		execCtx, toolSpan := obs.StartToolSpan(execCtx, pendingCall.Name, pendingCall.ID)
		start := time.Now()
		res, terr := t.Execute(execCtx, pendingCall.Arguments)
		ok2 := terr == nil && !res.IsError
		obs.RecordToolOutcome(execCtx, pendingCall.Name, ok2, time.Since(start))
		toolSpan.End()
		content := res.Content
		if terr != nil {
			content = terr.Error()
		}
		// 06 番設計書 全ツール出力に untrusted マーカーを無条件付与する
		// 旧実装は "[UNTRUSTED INPUT" 接頭辞ありで skip していたが、ツールが攻撃的に
		// 自前マーカーを偽装した場合に外周のラップを回避できる穴があったため常に付与する
		content = wrapUntrusted(content, pendingCall.Name)
		if s.redactor != nil {
			content = s.redactor.Redact(content)
		}
		tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: content, IsError: terr != nil || res.IsError}
		out <- Event{Kind: EventToolResult, ToolResult: tr}
		msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
	}
	err = fmt.Errorf("max tool hops を超えました (%d)", in.MaxToolHops)
	out <- Event{Kind: EventError, Err: err}
	return err
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
