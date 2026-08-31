package main

import (
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

func TestBuildAuditEmitterRejectsRemotePlaintextURL(t *testing.T) {
	t.Setenv("IGGY_PAT", "x")
	cfg := &config.Config{}
	cfg.Audit.IggyURL = "http://10.0.0.1:3000"
	if buildAuditEmitter(cfg, nil) != nil {
		t.Fatal("remote http:// must disable audit instead of sending credentials in cleartext")
	}
	cfg.Audit.IggyURL = "https://iggy.example.com"
	if buildAuditEmitter(cfg, nil) == nil {
		t.Fatal("https must be accepted")
	}
}
