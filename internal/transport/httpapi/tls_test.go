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
		in      string
		want    uint16
		wantErr bool
	}{
		// 空文字は TLS 1.2 既定、1.2/1.3 は明示的に許可
		// TLS 1.0 / 1.1 や未知の値は設定ミスとして error を返す
		{"1.2", tls.VersionTLS12, false},
		{"1.3", tls.VersionTLS13, false},
		{"", tls.VersionTLS12, false},
		{"1.0", 0, true},
		{"1.1", 0, true},
		{"bogus", 0, true},
	}
	for _, c := range cases {
		got, err := resolveMinVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveMinVersion(%q) want error got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveMinVersion(%q) unexpected err: %v", c.in, err)
			continue
		}
		if got != c.want {
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
	// HS256 必須長 (32 バイト) を満たすシークレットを与える
	v, err := NewJWTVerifier(JWTVerifierConfig{Enabled: true, SharedSecretEnv: "X"}, func(_ string) (string, bool) {
		return "this-secret-must-be-at-least-32-bytes-long", true
	})
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("verifier must not be nil")
	}
}

// TestNewJWTVerifier_RejectsShortSecret HS256 鍵長 32 バイト未満を拒否することを確認する
// RFC 7518 §3.2 で MUST とされている要件のため起動時 fail-fast で弾く
func TestNewJWTVerifier_RejectsShortSecret(t *testing.T) {
	t.Parallel()
	_, err := NewJWTVerifier(JWTVerifierConfig{Enabled: true, SharedSecretEnv: "X"}, func(_ string) (string, bool) {
		return "tooshort", true
	})
	if err == nil {
		t.Fatal("short HS256 secret must be rejected")
	}
}
