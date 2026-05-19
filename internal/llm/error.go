package llm

import "fmt"

// ProviderError プロバイダー由来のエラー
type ProviderError struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Underlying error
}

// Error error インターフェース実装
func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %s status=%d retryable=%v: %v", e.Provider, e.StatusCode, e.Retryable, e.Underlying)
}

// Unwrap 内部エラーを返す
func (e *ProviderError) Unwrap() error { return e.Underlying }
