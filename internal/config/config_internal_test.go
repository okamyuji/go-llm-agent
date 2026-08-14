package config

import "testing"

func TestValidateToolResultLimit_ZeroIsValid(t *testing.T) {
	if err := validateToolResultLimit(ToolResultLimitConfig{MaxChars: 0}); err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
}

func TestValidateToolResultLimit_PositiveIsValid(t *testing.T) {
	if err := validateToolResultLimit(ToolResultLimitConfig{MaxChars: 8000}); err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
}

func TestValidateToolResultLimit_MinusOneIsValid(t *testing.T) {
	if err := validateToolResultLimit(ToolResultLimitConfig{MaxChars: -1}); err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
}

func TestValidateToolResultLimit_BelowMinusOneIsRejected(t *testing.T) {
	if err := validateToolResultLimit(ToolResultLimitConfig{MaxChars: -2}); err == nil {
		t.Fatal("want err for max_chars=-2")
	}
}

func TestApplyDefaults_ToolResultLimitZeroBecomesDefault(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Agent.ToolResultLimit.MaxChars != defaultToolResultLimitMaxChars {
		t.Fatalf("got %d, want %d", cfg.Agent.ToolResultLimit.MaxChars, defaultToolResultLimitMaxChars)
	}
}

func TestApplyDefaults_ToolResultLimitMinusOneKept(t *testing.T) {
	cfg := &Config{Agent: AgentConfig{ToolResultLimit: ToolResultLimitConfig{MaxChars: -1}}}
	applyDefaults(cfg)
	if cfg.Agent.ToolResultLimit.MaxChars != -1 {
		t.Fatalf("got %d, want -1 (unchanged)", cfg.Agent.ToolResultLimit.MaxChars)
	}
}

func TestApplyDefaults_ToolResultLimitPositiveKept(t *testing.T) {
	cfg := &Config{Agent: AgentConfig{ToolResultLimit: ToolResultLimitConfig{MaxChars: 5000}}}
	applyDefaults(cfg)
	if cfg.Agent.ToolResultLimit.MaxChars != 5000 {
		t.Fatalf("got %d, want 5000 (unchanged)", cfg.Agent.ToolResultLimit.MaxChars)
	}
}

func TestValidateCompaction_NilEnabledIsSkipped(t *testing.T) {
	if err := validateCompaction(CompactionConfig{}); err != nil {
		t.Fatalf("Load を経ない構造体は検査しない期待 got %v", err)
	}
}

// validateCompaction の境界値を直接表明する。enabled=true の各フィールドについて
// 受理側と拒否側の両端を並べ、比較演算子の取り違えを検出できるようにする
func TestValidateCompaction_Boundaries(t *testing.T) {
	enabled := true
	base := func() CompactionConfig {
		return CompactionConfig{
			Enabled:             &enabled,
			ContextWindowTokens: 100,
			TriggerRatio:        0.5,
			KeepRecentTurns:     2,
		}
	}
	tests := []struct {
		name    string
		mutate  func(*CompactionConfig)
		wantErr bool
	}{
		{"context_window_tokens=1 は受理", func(c *CompactionConfig) { c.ContextWindowTokens = 1 }, false},
		{"context_window_tokens=0 は拒否", func(c *CompactionConfig) { c.ContextWindowTokens = 0 }, true},
		{"context_window_tokens=-1 は拒否", func(c *CompactionConfig) { c.ContextWindowTokens = -1 }, true},
		{"trigger_ratio=1 は受理", func(c *CompactionConfig) { c.TriggerRatio = 1 }, false},
		{"trigger_ratio=1.0001 は拒否", func(c *CompactionConfig) { c.TriggerRatio = 1.0001 }, true},
		{"trigger_ratio=0.0001 は受理", func(c *CompactionConfig) { c.TriggerRatio = 0.0001 }, false},
		{"trigger_ratio=0 は拒否", func(c *CompactionConfig) { c.TriggerRatio = 0 }, true},
		{"trigger_ratio=-0.1 は拒否", func(c *CompactionConfig) { c.TriggerRatio = -0.1 }, true},
		{"keep_recent_turns=0 は受理", func(c *CompactionConfig) { c.KeepRecentTurns = 0 }, false},
		{"keep_recent_turns=-1 は拒否", func(c *CompactionConfig) { c.KeepRecentTurns = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			err := validateCompaction(c)
			if tt.wantErr && err == nil {
				t.Fatalf("エラー期待だが nil (cfg=%+v)", c)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("nil 期待だが %v (cfg=%+v)", err, c)
			}
		})
	}
}

// enabled=false のとき値が不正でも検査を通す挙動を表明する
func TestValidateCompaction_DisabledSkipsValidation(t *testing.T) {
	disabled := false
	c := CompactionConfig{Enabled: &disabled, ContextWindowTokens: -5, TriggerRatio: -1, KeepRecentTurns: -3}
	if err := validateCompaction(c); err != nil {
		t.Fatalf("enabled=false は検査しない期待 got %v", err)
	}
}

func TestApplyCompactionDefaults_KeepsExplicitFalse(t *testing.T) {
	disabled := false
	c := CompactionConfig{Enabled: &disabled}
	applyCompactionDefaults(&c)
	if *c.Enabled {
		t.Fatal("明示 false を上書きしない期待")
	}
	if c.ContextWindowTokens != defaultCompactionContextWindowTokens {
		t.Fatalf("数値既定値は適用する期待 got %d", c.ContextWindowTokens)
	}
}
