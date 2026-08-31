package audit

import (
	"context"
	"testing"
)

func TestSessionIDRoundTrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "abc-1")
	if got := SessionIDFrom(ctx); got != "abc-1" {
		t.Fatalf("got %q", got)
	}
	if got := SessionIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestNormalizeSessionID(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		replaced bool
	}{
		{"abc_DEF-123", "abc_DEF-123", false},
		{"", "run-r1", true},
		{"../x", "run-r1", true},
		{"a:b", "run-r1", true},
		{string(make([]byte, 129)), "run-r1", true},
	}
	for _, c := range cases {
		got, rep := NormalizeSessionID(c.in, "r1")
		if got != c.want || rep != c.replaced {
			t.Errorf("NormalizeSessionID(%q) = %q,%v want %q,%v", c.in, got, rep, c.want, c.replaced)
		}
	}
}
