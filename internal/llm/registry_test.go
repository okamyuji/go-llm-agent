package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("not used")
}
func (f *fakeProvider) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	return nil, errors.New("not used")
}

func TestRegistry_Resolve(t *testing.T) {
	r := llm.NewRegistry(map[string]llm.Provider{
		"openai":    &fakeProvider{name: "openai"},
		"anthropic": &fakeProvider{name: "anthropic"},
	})

	p, model, err := r.Resolve("openai/gpt-4.1-mini")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Name() != "openai" {
		t.Fatalf("provider name")
	}
	if model != "gpt-4.1-mini" {
		t.Fatalf("model name got=%q", model)
	}
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	r := llm.NewRegistry(map[string]llm.Provider{})
	if _, _, err := r.Resolve("foo/bar"); err == nil {
		t.Fatal("unknown provider err")
	}
	if _, _, err := r.Resolve("noslash"); err == nil {
		t.Fatal("slash required")
	}
}

func TestProviderError_String(t *testing.T) {
	e := &llm.ProviderError{Provider: "openai", StatusCode: 500, Retryable: true, Underlying: errors.New("x")}
	if e.Error() == "" {
		t.Fatal("error string")
	}
	if !errors.Is(e, e.Underlying) && errors.Unwrap(e).Error() != "x" {
		t.Fatal("unwrap")
	}
}

func TestRegistry_AllowModels_AcceptsListed(t *testing.T) {
	r := llm.NewRegistryWithAllowlist(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		map[string][]string{"openai": {"gpt-4.1-mini", "gpt-4o-mini"}},
	)
	if _, _, err := r.Resolve("openai/gpt-4.1-mini"); err != nil {
		t.Fatalf("許可モデルは通る: %v", err)
	}
}

func TestRegistry_AllowModels_RejectsUnlisted(t *testing.T) {
	r := llm.NewRegistryWithAllowlist(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		map[string][]string{"openai": {"gpt-4.1-mini"}},
	)
	_, _, err := r.Resolve("openai/gpt-3.5")
	if err == nil {
		t.Fatal("許可外モデルは拒否されるべき")
	}
}

func TestRegistry_AllowModels_EmptyMeansAll(t *testing.T) {
	r := llm.NewRegistryWithAllowlist(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		map[string][]string{"openai": {}},
	)
	if _, _, err := r.Resolve("openai/anything"); err != nil {
		t.Fatalf("空 allow_models は全許可: %v", err)
	}
}

func TestRegistry_ResolveWithFallback_ReturnsBoth(t *testing.T) {
	r := llm.NewRegistryWithFallback(
		map[string]llm.Provider{
			"openai":    &fakeProvider{name: "openai"},
			"anthropic": &fakeProvider{name: "anthropic"},
		},
		nil,
		map[string]string{"openai": "anthropic"},
	)
	primary, pmodel, fb, fbmodel, err := r.ResolveWithFallback("openai/gpt-4")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if primary.Name() != "openai" || pmodel != "gpt-4" {
		t.Errorf("primary unexpected: name=%q model=%q", primary.Name(), pmodel)
	}
	if fb == nil || fb.Name() != "anthropic" || fbmodel != "gpt-4" {
		t.Errorf("fallback unexpected: fb=%v model=%q", fb, fbmodel)
	}
}

func TestRegistry_ResolveWithFallback_NoFallbackReturnsNil(t *testing.T) {
	r := llm.NewRegistryWithFallback(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		nil,
		nil,
	)
	_, _, fb, _, err := r.ResolveWithFallback("openai/gpt-4")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if fb != nil {
		t.Errorf("fallback must be nil when not configured, got %v", fb)
	}
}

func TestRegistry_ResolveWithFallback_InvalidModelString(t *testing.T) {
	r := llm.NewRegistryWithFallback(map[string]llm.Provider{}, nil, nil)
	if _, _, _, _, err := r.ResolveWithFallback("noslash"); err == nil {
		t.Fatal("invalid model must error")
	}
}

func TestRegistry_ResolveWithFallback_UnknownPrimary(t *testing.T) {
	r := llm.NewRegistryWithFallback(map[string]llm.Provider{}, nil, nil)
	if _, _, _, _, err := r.ResolveWithFallback("missing/gpt"); err == nil {
		t.Fatal("unknown primary must error")
	}
}

func TestRegistry_ResolveWithFallback_AllowModelsRejection(t *testing.T) {
	r := llm.NewRegistryWithFallback(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		map[string][]string{"openai": {"only-this"}},
		nil,
	)
	if _, _, _, _, err := r.ResolveWithFallback("openai/forbidden"); err == nil {
		t.Fatal("disallowed model must error")
	}
}

func TestRegistry_ResolveWithFallback_FallbackNameUnknownReturnsNil(t *testing.T) {
	r := llm.NewRegistryWithFallback(
		map[string]llm.Provider{"openai": &fakeProvider{name: "openai"}},
		nil,
		map[string]string{"openai": "missing"},
	)
	_, _, fb, _, err := r.ResolveWithFallback("openai/gpt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if fb != nil {
		t.Error("fallback to unknown provider must yield nil")
	}
}
