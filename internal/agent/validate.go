package agent

import (
	"encoding/json"
	"fmt"

	"github.com/xeipuuv/gojsonschema"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// SchemaValidator ツール引数を JSON Schema で検証するインターフェース
type SchemaValidator interface {
	Validate(toolName string, args json.RawMessage) (ok bool, msg string)
}

// schemaValidator gojsonschema を使う実装
type schemaValidator struct {
	schemas map[string]*gojsonschema.Schema
}

// NewSchemaValidator tool.Registry に登録されたスキーマからバリデータを構築する
// スキーマ未指定または不正なツールは「常に通過」として扱う
func NewSchemaValidator(reg tool.Registry) SchemaValidator {
	out := &schemaValidator{schemas: map[string]*gojsonschema.Schema{}}
	for _, sp := range reg.List() {
		if len(sp.Schema) == 0 {
			continue
		}
		loader := gojsonschema.NewBytesLoader(sp.Schema)
		sch, err := gojsonschema.NewSchema(loader)
		if err != nil {
			continue
		}
		out.schemas[sp.Name] = sch
	}
	return out
}

// Validate args が toolName のスキーマに合致するか判定する
// ok=false のとき msg に人間可読な理由を返す
func (v *schemaValidator) Validate(toolName string, args json.RawMessage) (bool, string) {
	sch, ok := v.schemas[toolName]
	if !ok {
		return true, ""
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := sch.Validate(gojsonschema.NewBytesLoader(args))
	if err != nil {
		return false, fmt.Sprintf("validate failed: %v", err)
	}
	if res.Valid() {
		return true, ""
	}
	msg := ""
	for _, e := range res.Errors() {
		if msg != "" {
			msg += "; "
		}
		msg += e.String()
	}
	return false, msg
}
