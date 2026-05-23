package safety

import "testing"

func TestPIIRedactor_DisabledIsNoop(t *testing.T) {
	t.Parallel()
	r, err := NewPIIRedactor(PIIRedactorConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Redact("a@example.com"); got != "a@example.com" {
		t.Errorf("disabled must be noop, got %q", got)
	}
}

func TestPIIRedactor_MaskEmail(t *testing.T) {
	t.Parallel()
	r, err := NewPIIRedactor(PIIRedactorConfig{
		Enabled: true,
		Rules: []PIIRule{
			{ID: "email", Regex: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`, Replacement: "[REDACTED:EMAIL]"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Redact("contact me at alice@example.com today")
	if !contains(got, "[REDACTED:EMAIL]") {
		t.Errorf("expected mask in %q", got)
	}
}

func TestPIIRedactor_MaskJPPhoneAndIPv4(t *testing.T) {
	t.Parallel()
	r, err := NewPIIRedactor(PIIRedactorConfig{
		Enabled: true,
		Rules: []PIIRule{
			{ID: "phone", Regex: `0\d{1,4}-\d{1,4}-\d{3,4}`, Replacement: "[REDACTED:PHONE]"},
			{ID: "ipv4", Regex: `\b(?:\d{1,3}\.){3}\d{1,3}\b`, Replacement: "[REDACTED:IPV4]"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Redact("call 03-1234-5678 from 10.0.0.5")
	if !contains(got, "[REDACTED:PHONE]") || !contains(got, "[REDACTED:IPV4]") {
		t.Errorf("expected both masks, got %q", got)
	}
}

func TestPIIRedactor_InvalidRegexErrors(t *testing.T) {
	t.Parallel()
	_, err := NewPIIRedactor(PIIRedactorConfig{Enabled: true, Rules: []PIIRule{{ID: "bad", Regex: "([", Replacement: "x"}}})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestChainRedactor_WithPIIAndOutput(t *testing.T) {
	t.Parallel()
	out, _ := NewRedactorFromConfig(OutputRedactorConfig{Enabled: true, Rules: []OutputRedactorRule{{ID: "key", Regex: `sk-[A-Z0-9]+`, Replacement: "[KEY]"}}})
	pii, _ := NewPIIRedactor(PIIRedactorConfig{Enabled: true, Rules: []PIIRule{{ID: "email", Regex: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`, Replacement: "[EMAIL]"}}})
	chained := ChainRedactor(out, pii)
	got := chained.Redact("k=sk-AAA mail=a@b.com")
	if !contains(got, "[KEY]") || !contains(got, "[EMAIL]") {
		t.Errorf("chain redactor missed: %q", got)
	}
}
