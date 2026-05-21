package llm

import (
	"fmt"
	"slices"
	"strings"
)

// Registry model 文字列から Provider を引く
type Registry interface {
	Resolve(model string) (Provider, string, error)
	List() []string
}

type registry struct {
	providers   map[string]Provider
	allowModels map[string][]string
}

// NewRegistry プロバイダー名 to Provider の map から Registry を生成
// モデル許可リストは未指定として扱う
func NewRegistry(providers map[string]Provider) Registry {
	return NewRegistryWithAllowlist(providers, nil)
}

// NewRegistryWithAllowlist プロバイダーごとの許可モデルリスト付きで Registry を生成
// allow[provider] が非空の場合のみ照合される
func NewRegistryWithAllowlist(providers map[string]Provider, allow map[string][]string) Registry {
	if allow == nil {
		allow = map[string][]string{}
	}
	return &registry{providers: providers, allowModels: allow}
}

// Resolve "openai/gpt-4.1-mini" 形式の文字列を Provider と純モデル名に分解
// プロバイダーごとの allow_models が設定されている場合は照合する
func (r *registry) Resolve(model string) (Provider, string, error) {
	pname, name, ok := strings.Cut(model, "/")
	if !ok || pname == "" || name == "" {
		return nil, "", fmt.Errorf("model は provider/name 形式である必要があります got=%q", model)
	}
	p, ok := r.providers[pname]
	if !ok {
		return nil, "", fmt.Errorf("provider %q は登録されていません", pname)
	}
	if allow := r.allowModels[pname]; len(allow) > 0 {
		if !slices.Contains(allow, name) {
			return nil, "", fmt.Errorf("model %q は provider %q の allow_models に含まれていません", name, pname)
		}
	}
	return p, name, nil
}

// List 登録されているプロバイダー名一覧
func (r *registry) List() []string {
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	return out
}
