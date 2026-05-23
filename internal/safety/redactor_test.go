package safety

import "testing"

func TestNewRedactor_DisabledIsNoop(t *testing.T) {
	t.Parallel()
	r, err := NewRedactorFromConfig(OutputRedactorConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	in := "my key is sk-XYZ"
	if r.Redact(in) != in {
		t.Errorf("disabled redactor must be noop")
	}
}

func TestRedactor_MasksOpenAIKey(t *testing.T) {
	t.Parallel()
	r, err := NewRedactorFromConfig(OutputRedactorConfig{
		Enabled: true,
		Rules: []OutputRedactorRule{
			{ID: "openai_key", Regex: `sk-[A-Za-z0-9]{16,}`, Replacement: "[REDACTED:OPENAI]"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Redact("the key is sk-ABCDEFGHIJKLMNOP1234 and not safe")
	if got == "the key is sk-ABCDEFGHIJKLMNOP1234 and not safe" {
		t.Fatal("redactor did not mask")
	}
	if !contains(got, "[REDACTED:OPENAI]") {
		t.Errorf("expected mask in: %s", got)
	}
}

func TestRedactor_MasksMultipleSecrets(t *testing.T) {
	t.Parallel()
	r, err := NewRedactorFromConfig(OutputRedactorConfig{
		Enabled: true,
		Rules: []OutputRedactorRule{
			{ID: "openai", Regex: `sk-[A-Za-z0-9]{16,}`, Replacement: "[OK]"},
			{ID: "jwt", Regex: `eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`, Replacement: "[JWT]"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Redact("k=sk-AAAAAAAAAAAAAAAA1 token=eyJhbGciOi.eyJzdWIiOi.signature_part_here")
	if !contains(got, "[OK]") || !contains(got, "[JWT]") {
		t.Errorf("expected both masks in: %s", got)
	}
}

func TestRedactor_InvalidRegexReturnsError(t *testing.T) {
	t.Parallel()
	_, err := NewRedactorFromConfig(OutputRedactorConfig{
		Enabled: true,
		Rules:   []OutputRedactorRule{{ID: "bad", Regex: "([)", Replacement: "x"}},
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestChainRedactor_AppliesAllInOrder(t *testing.T) {
	t.Parallel()
	r1, _ := NewRedactorFromConfig(OutputRedactorConfig{Enabled: true, Rules: []OutputRedactorRule{{ID: "a", Regex: `secret`, Replacement: "X"}}})
	r2, _ := NewRedactorFromConfig(OutputRedactorConfig{Enabled: true, Rules: []OutputRedactorRule{{ID: "b", Regex: `X`, Replacement: "Y"}}})
	chained := ChainRedactor(r1, r2)
	if got := chained.Redact("this secret"); got != "this Y" {
		t.Errorf("got %q want 'this Y'", got)
	}
}

func TestChainRedactor_NilEntriesAreSkipped(t *testing.T) {
	t.Parallel()
	c := ChainRedactor(nil, nil)
	if got := c.Redact("hello"); got != "hello" {
		t.Errorf("nil-only chain must be noop, got %q", got)
	}
}

func TestChainRedactor_SingleReturnsAsIs(t *testing.T) {
	t.Parallel()
	r, _ := NewRedactorFromConfig(OutputRedactorConfig{Enabled: true, Rules: []OutputRedactorRule{{ID: "a", Regex: `a`, Replacement: "b"}}})
	c := ChainRedactor(r)
	if got := c.Redact("ab"); got != "bb" {
		t.Errorf("got %q want 'bb'", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
