package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

type upperRedactor struct{}

func (upperRedactor) Redact(s string) string { return strings.ReplaceAll(s, "SECRET", "[R]") }

func TestRedactJSONRecursive(t *testing.T) {
	in := json.RawMessage(`{"SECRET_key":"SECRET value","n":42,"b":true,"z":null,"arr":["SECRET",{"deep":"SECRET"}]}`)
	got := RedactJSON(in, upperRedactor{})
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("output must stay valid JSON: %v (%s)", err, got)
	}
	if _, ok := v["[R]_key"]; !ok {
		t.Errorf("key not redacted: %s", got)
	}
	if v["[R]_key"] != "[R] value" {
		t.Errorf("string value not redacted: %s", got)
	}
	if v["n"] != float64(42) || v["b"] != true || v["z"] != nil {
		t.Errorf("non-string values must be unchanged: %s", got)
	}
	arr := v["arr"].([]any)
	if arr[0] != "[R]" || arr[1].(map[string]any)["deep"] != "[R]" {
		t.Errorf("nested values not redacted: %s", got)
	}
}

func TestRedactJSONInvalidFallsBackToString(t *testing.T) {
	got := RedactJSON(json.RawMessage(`not json SECRET`), upperRedactor{})
	var s string
	if err := json.Unmarshal(got, &s); err != nil || s != "not json [R]" {
		t.Fatalf("got %s (%v)", got, err)
	}
}

func TestRedactNilRedactorPassthrough(t *testing.T) {
	in := json.RawMessage(`{"a":"SECRET"}`)
	if string(RedactJSON(in, nil)) != string(in) {
		t.Fatal("nil redactor must pass through")
	}
	if RedactString("SECRET", nil) != "SECRET" {
		t.Fatal("nil redactor must pass through string")
	}
}
