package httpapi

import (
	"crypto/tls"
	"testing"
)

func TestBuildTLSConfig_DisabledReturnsNil(t *testing.T) {
	t.Parallel()
	got, err := BuildTLSConfig(TLSConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("disabled must return nil")
	}
}

func TestBuildTLSConfig_RequiresCertAndKey(t *testing.T) {
	t.Parallel()
	if _, err := BuildTLSConfig(TLSConfig{Enabled: true}); err == nil {
		t.Fatal("expected error for missing cert/key")
	}
}

func TestResolveMinVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want uint16
	}{
		{"1.0", tls.VersionTLS10},
		{"1.1", tls.VersionTLS11},
		{"1.2", tls.VersionTLS12},
		{"1.3", tls.VersionTLS13},
		{"", tls.VersionTLS13},
		{"bogus", tls.VersionTLS13},
	}
	for _, c := range cases {
		if got := resolveMinVersion(c.in); got != c.want {
			t.Errorf("resolveMinVersion(%q) = %d want %d", c.in, got, c.want)
		}
	}
}

func TestNewJWTVerifier_DisabledReturnsNil(t *testing.T) {
	t.Parallel()
	v, err := NewJWTVerifier(JWTVerifierConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Error("disabled must return nil verifier")
	}
}

func TestNewJWTVerifier_RequiresSharedSecret(t *testing.T) {
	t.Parallel()
	_, err := NewJWTVerifier(JWTVerifierConfig{Enabled: true}, func(string) (string, bool) { return "", true })
	if err == nil {
		t.Fatal("expected error when shared_secret_env is empty")
	}
}

func TestNewJWTVerifier_BuildsWithSharedSecret(t *testing.T) {
	t.Parallel()
	v, err := NewJWTVerifier(JWTVerifierConfig{Enabled: true, SharedSecretEnv: "X"}, func(env string) (string, bool) {
		return "topsecret", true
	})
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("verifier must not be nil")
	}
}
