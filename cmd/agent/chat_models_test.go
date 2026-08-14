package main

import (
	"reflect"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

func TestAvailableModels(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want map[string][]string
	}{
		{
			name: "プロバイダーなし",
			cfg:  &config.Config{},
			want: map[string][]string{},
		},
		{
			name: "allow_models 未設定は nil のまま (制限なしとして表示される)",
			cfg: &config.Config{Providers: map[string]config.ProviderConfig{
				"llama": {},
			}},
			want: map[string][]string{"llama": nil},
		},
		{
			name: "allow_models 指定ありは列挙をそのまま渡す",
			cfg: &config.Config{Providers: map[string]config.ProviderConfig{
				"openai": {AllowModels: []string{"gpt-4.1-mini", "gpt-4.1"}},
				"llama":  {AllowModels: []string{}},
			}},
			want: map[string][]string{
				"openai": {"gpt-4.1-mini", "gpt-4.1"},
				"llama":  {},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := availableModels(tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("availableModels()=%#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestAvailableModels_DoesNotAliasConfigSlice 返り値の変更が config の
// allow_models を壊さないこと (REPL へ渡した後に表示側で書き換わらない保証)
func TestAvailableModels_DoesNotAliasConfigSlice(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"openai": {AllowModels: []string{"gpt-4.1"}},
	}}
	got := availableModels(cfg)
	got["openai"][0] = "changed"
	if cfg.Providers["openai"].AllowModels[0] != "gpt-4.1" {
		t.Fatalf("config の allow_models が書き換わった: %v", cfg.Providers["openai"].AllowModels)
	}
}
