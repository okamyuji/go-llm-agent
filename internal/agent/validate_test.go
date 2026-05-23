package agent

import (
	"encoding/json"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// stubReg validate_test 専用の tool.Registry スタブ
type stubReg struct{ specs []tool.Spec }

func (r *stubReg) Lookup(string) (tool.Tool, bool) { return nil, false }
func (r *stubReg) List() []tool.Spec               { return r.specs }

func TestSchemaValidator_AcceptsValidArgs(t *testing.T) {
	t.Parallel()
	reg := &stubReg{specs: []tool.Spec{{
		Name:        "fs_read",
		Description: "read",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}}
	v := NewSchemaValidator(reg)
	ok, msg := v.Validate("fs_read", json.RawMessage(`{"path":"a.txt"}`))
	if !ok {
		t.Fatalf("expected ok, got msg=%q", msg)
	}
}

func TestSchemaValidator_RejectsMissingRequired(t *testing.T) {
	t.Parallel()
	reg := &stubReg{specs: []tool.Spec{{
		Name:   "fs_read",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}}
	v := NewSchemaValidator(reg)
	ok, msg := v.Validate("fs_read", json.RawMessage(`{}`))
	if ok {
		t.Fatal("expected validation failure for missing required field")
	}
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestSchemaValidator_RejectsWrongType(t *testing.T) {
	t.Parallel()
	reg := &stubReg{specs: []tool.Spec{{
		Name:   "fs_read",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}}
	v := NewSchemaValidator(reg)
	ok, _ := v.Validate("fs_read", json.RawMessage(`{"path":123}`))
	if ok {
		t.Fatal("expected validation failure for wrong type")
	}
}

func TestSchemaValidator_UnknownToolPasses(t *testing.T) {
	t.Parallel()
	reg := &stubReg{}
	v := NewSchemaValidator(reg)
	ok, _ := v.Validate("nonexistent", json.RawMessage(`{}`))
	if !ok {
		t.Fatal("unknown tool must pass")
	}
}

func TestSchemaValidator_EmptySchemaIsSkipped(t *testing.T) {
	t.Parallel()
	reg := &stubReg{specs: []tool.Spec{{Name: "noopaque"}}}
	v := NewSchemaValidator(reg)
	ok, _ := v.Validate("noopaque", json.RawMessage(`{"any":1}`))
	if !ok {
		t.Fatal("empty schema must skip validation")
	}
}

func TestSchemaValidator_EmptyArgsBecomesEmptyObject(t *testing.T) {
	t.Parallel()
	reg := &stubReg{specs: []tool.Spec{{
		Name:   "fs_read",
		Schema: json.RawMessage(`{"type":"object"}`),
	}}}
	v := NewSchemaValidator(reg)
	ok, _ := v.Validate("fs_read", nil)
	if !ok {
		t.Fatal("empty args must be treated as {} and pass type:object schema")
	}
}
