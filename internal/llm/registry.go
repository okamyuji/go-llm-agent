package llm

import (
	"fmt"
	"strings"
)

// Registry model 文字列から Provider を引く
type Registry interface {
	Resolve(model string) (Provider, string, error)
	List() []string
}

type registry struct {
	providers map[string]Provider
}

// NewRegistry プロバイダー名 to Provider の map から Registry を生成
func NewRegistry(providers map[string]Provider) Registry {
	return &registry{providers: providers}
}

// Resolve "openai/gpt-4.1-mini" 形式の文字列を Provider と純モデル名に分解
func (r *registry) Resolve(model string) (Provider, string, error) {
	pname, name, ok := strings.Cut(model, "/")
	if !ok || pname == "" || name == "" {
		return nil, "", fmt.Errorf("model は provider/name 形式である必要があります got=%q", model)
	}
	p, ok := r.providers[pname]
	if !ok {
		return nil, "", fmt.Errorf("provider %q は登録されていません", pname)
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
