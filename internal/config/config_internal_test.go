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
