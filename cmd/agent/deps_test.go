package main

import (
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

func TestBuildAuditEmitterDisabledWithoutPAT(t *testing.T) {
	t.Setenv("IGGY_PAT", "")
	if e := buildAuditEmitter(&config.Config{}, nil); e != nil {
		t.Fatal("emitter must be nil without IGGY_PAT")
	}
	t.Setenv("IGGY_PAT", "x")
	if e := buildAuditEmitter(&config.Config{}, nil); e == nil {
		t.Fatal("emitter must be created with IGGY_PAT")
	}
}
