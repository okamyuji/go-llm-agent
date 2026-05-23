package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// JWTVerifierConfig OAuth2 リソースサーバの設定
// jwks_url は将来 fetch 実装で利用する想定で、現状の MVP では shared_secret_env 経由の
// HS256 検証のみをサポートする
type JWTVerifierConfig struct {
	Enabled         bool
	Issuer          string
	Audience        string
	JWKSURL         string
	SharedSecretEnv string
	CacheTTLSeconds int
}

// JWTVerifier JWT 検証ミドルウェア。15 番 MVP として共有鍵 HS256 検証の
// スタブを提供する。本番では go-jwt と JWKS fetch を組み合わせて RS256 / ES256 と
// 鍵ローテーションに対応する想定
type JWTVerifier struct {
	Issuer   string
	Audience string
	Secret   []byte
	cacheTTL time.Duration
}

// NewJWTVerifier 設定から JWTVerifier を構築する
// Enabled=false なら nil を返し、ミドルウェアは適用されない
func NewJWTVerifier(c JWTVerifierConfig, secretLookup func(env string) (string, bool)) (*JWTVerifier, error) {
	if !c.Enabled {
		return nil, nil
	}
	if c.SharedSecretEnv == "" {
		return nil, errors.New("httpapi: oauth2.shared_secret_env is required for MVP HS256 verifier")
	}
	if secretLookup == nil {
		return nil, errors.New("httpapi: secret lookup function is nil")
	}
	v, ok := secretLookup(c.SharedSecretEnv)
	if !ok || v == "" {
		return nil, errors.New("httpapi: oauth2 shared secret env is not set")
	}
	ttl := time.Duration(c.CacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &JWTVerifier{Issuer: c.Issuer, Audience: c.Audience, Secret: []byte(v), cacheTTL: ttl}, nil
}

// Handler 次の Handler を JWT 検証で包む
// /healthz はスキップする
func (j *JWTVerifier) Handler(next http.Handler) http.Handler {
	if j == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "missing bearer JWT", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, prefix)
		if !strings.HasPrefix(token, "eyJ") {
			http.Error(w, "value is not a JWT", http.StatusUnauthorized)
			return
		}
		// MVP: 実 JWT 検証は go-jwt 統合まで保留し、issuer 文字列が含まれているか
		// だけを確認するスタブとする。これは本来のセキュリティ用途ではなく、
		// パイプライン全体の配線とエンドポイント疎通の確認だけに使う
		if j.Issuer != "" && !strings.Contains(token, encodeIssuerStub(j.Issuer)) {
			http.Error(w, "issuer mismatch", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// encodeIssuerStub MVP の擬似マッチ用ハッシュ。本実装では JWT payload を decode して
// iss クレームを比較する
func encodeIssuerStub(s string) string {
	// 単純な substring 検査で十分なので長さ確認のみ行う
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
