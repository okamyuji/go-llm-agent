package tool

// correlationKey audit ログの相関 ID を context に積むための型
// パッケージ外からは CorrelationKey() を経由してアクセスする
type correlationKey struct{}

// CorrelationKey audit ログ間で agent ループの hop を紐付けるための context key
// agent.loop は tool.Execute 直前に context.WithValue(ctx, tool.CorrelationKey(), <tool_call_id>) を行う
func CorrelationKey() any { return correlationKey{} }
