package llm

import (
	"fmt"
	"slices"
	"strings"
)

// Registry model 文字列から Provider を引く
type Registry interface {
	Resolve(model string) (Provider, string, error)
	ResolveWithFallback(model string) (primary Provider, primaryModel string, fallback Provider, fallbackModel string, err error)
	List() []string
}

type registry struct {
	providers   map[string]Provider
	allowModels map[string][]string
	fallback    map[string]string
}

// NewRegistry プロバイダー名 to Provider の map から Registry を生成
// モデル許可リストとフォールバック設定は未指定として扱う
func NewRegistry(providers map[string]Provider) Registry {
	return NewRegistryWithAllowlist(providers, nil)
}

// NewRegistryWithAllowlist プロバイダーごとの許可モデルリスト付きで Registry を生成
// allow[provider] が非空の場合のみ照合される
func NewRegistryWithAllowlist(providers map[string]Provider, allow map[string][]string) Registry {
	return NewRegistryWithFallback(providers, allow, nil)
}

// NewRegistryWithFallback 許可リストとフォールバック設定付きで Registry を生成する
// fallback[provider] には primary が失敗したときに切り替える別プロバイダー名を入れる
func NewRegistryWithFallback(providers map[string]Provider, allow map[string][]string, fallback map[string]string) Registry {
	if allow == nil {
		allow = map[string][]string{}
	}
	if fallback == nil {
		fallback = map[string]string{}
	}
	return &registry{providers: providers, allowModels: allow, fallback: fallback}
}

// parseAndLookup model 文字列 ("provider/name") を解析し許可リストを照合する内部ヘルパ
func (r *registry) parseAndLookup(model string) (Provider, string, string, error) {
	pname, name, ok := strings.Cut(model, "/")
	if !ok || pname == "" || name == "" {
		return nil, "", "", fmt.Errorf("model は provider/name 形式である必要があります got=%q", model)
	}
	p, ok := r.providers[pname]
	if !ok {
		return nil, "", "", fmt.Errorf("provider %q は登録されていません", pname)
	}
	if allow := r.allowModels[pname]; len(allow) > 0 && !slices.Contains(allow, name) {
		return nil, "", "", fmt.Errorf("model %q は provider %q の allow_models に含まれていません", name, pname)
	}
	return p, pname, name, nil
}

// Resolve "openai/gpt-4.1-mini" 形式の文字列を Provider と純モデル名に分解
// プロバイダーごとの allow_models が設定されている場合は照合する
func (r *registry) Resolve(model string) (Provider, string, error) {
	p, _, name, err := r.parseAndLookup(model)
	if err != nil {
		return nil, "", err
	}
	return p, name, nil
}

// ResolveWithFallback primary に加えて fallback プロバイダーも返す
// fallback が未設定なら fallbackProvider は nil で返る
// fallback プロバイダーは primary と同じモデル名を使い、要求モデル名で許可チェックを再度行う
func (r *registry) ResolveWithFallback(model string) (Provider, string, Provider, string, error) {
	primary, pname, name, err := r.parseAndLookup(model)
	if err != nil {
		return nil, "", nil, "", err
	}
	var fallback Provider
	var fallbackModel string
	if fbName, hasFB := r.fallback[pname]; hasFB && fbName != "" {
		fp, ok := r.providers[fbName]
		if !ok {
			// 未登録の fallback 先は nil 扱い。将来 logger 注入時に warn を出す
			return primary, name, nil, "", nil
		}
		// fallback 側の allow_models に model 名が含まれていなければ不許可モデル
		// 呼び出しを防ぐため fallback を nil で skip する
		if allow := r.allowModels[fbName]; len(allow) > 0 && !slices.Contains(allow, name) {
			return primary, name, nil, "", nil
		}
		fallback = fp
		fallbackModel = name
	}
	return primary, name, fallback, fallbackModel, nil
}

// List 登録されているプロバイダー名一覧
func (r *registry) List() []string {
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	return out
}
