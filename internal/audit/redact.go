package audit

import (
	"encoding/json"

	"github.com/okamyuji/go-llm-agent/internal/safety"
)

// RedactString r が nil なら素通し
func RedactString(s string, r safety.Redactor) string {
	if r == nil {
		return s
	}
	return r.Redact(s)
}

// RedactJSON JSON を再帰的に走査し、文字列値とオブジェクトのキーに redactor を通す。
// JSON 全体を 1 つの文字列として通すと、引用符をまたぐ一致で壊れた JSON になるため値単位で扱う。
// JSON として読めない入力は全体を 1 つの文字列として通し、JSON 文字列にして返す
func RedactJSON(raw json.RawMessage, r safety.Redactor) json.RawMessage {
	if r == nil || len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		b, _ := json.Marshal(r.Redact(string(raw)))
		return b
	}
	out, err := json.Marshal(redactValue(v, r))
	if err != nil {
		b, _ := json.Marshal(r.Redact(string(raw)))
		return b
	}
	return out
}

func redactValue(v any, r safety.Redactor) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[r.Redact(k)] = redactValue(val, r)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, val := range x {
			a[i] = redactValue(val, r)
		}
		return a
	default:
		return v
	}
}
