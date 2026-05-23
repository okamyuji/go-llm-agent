package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// SchemaValidator ツール引数を JSON Schema で検証するインターフェース
type SchemaValidator interface {
	Validate(toolName string, args json.RawMessage) (ok bool, msg string)
}

// schemaValidator santhosh-tekuri/jsonschema/v5 を使う実装
// 旧版 xeipuuv/gojsonschema は maintenance mode のため移行した
type schemaValidator struct {
	schemas map[string]*jsonschema.Schema
}

// NewSchemaValidator tool.Registry に登録されたスキーマからバリデータを構築する
// スキーマ未指定のツールは「常に通過」として扱う
// 不正な schema が含まれていた場合、起動時に error を返して fail-fast する
func NewSchemaValidator(reg tool.Registry) (SchemaValidator, error) {
	out := &schemaValidator{schemas: map[string]*jsonschema.Schema{}}
	for _, sp := range reg.List() {
		if len(sp.Schema) == 0 {
			continue
		}
		// schema を Compiler 経由でロードする。Compiler は schema 1 件ごとに新規作成して
		// ツール間で名前空間が混ざらないようにする
		compiler := jsonschema.NewCompiler()
		// schema 本体に id がない場合に備えて固定の URL を使う
		const schemaURL = "inline://tool-schema.json"
		if err := compiler.AddResource(schemaURL, strings.NewReader(string(sp.Schema))); err != nil {
			return nil, fmt.Errorf("agent: schema add resource failed for tool %q: %w", sp.Name, err)
		}
		sch, err := compiler.Compile(schemaURL)
		if err != nil {
			return nil, fmt.Errorf("agent: schema compile failed for tool %q: %w", sp.Name, err)
		}
		out.schemas[sp.Name] = sch
	}
	return out, nil
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
	// jsonschema/v5 は any 型インスタンスを検証する。json.RawMessage を一度デコードする
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		return false, fmt.Sprintf("invalid json: %v", err)
	}
	if err := sch.Validate(instance); err != nil {
		var verr *jsonschema.ValidationError
		if errors.As(err, &verr) {
			return false, summarizeValidationError(verr)
		}
		return false, fmt.Sprintf("validate failed: %v", err)
	}
	return true, ""
}

// summarizeValidationError ValidationError.BasicOutput からエラー要約文字列を作る
// 1 個目の root エラーと、最大 3 件の cause を ; 区切りで結合する
func summarizeValidationError(verr *jsonschema.ValidationError) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%s: %s", verr.InstanceLocation, verr.Message))
	const maxCauses = 3
	for i, c := range verr.Causes {
		if i >= maxCauses {
			parts = append(parts, fmt.Sprintf("(%d more)", len(verr.Causes)-maxCauses))
			break
		}
		parts = append(parts, fmt.Sprintf("%s: %s", c.InstanceLocation, c.Message))
	}
	return strings.Join(parts, "; ")
}
