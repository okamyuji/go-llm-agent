package httpapi

import (
	"errors"
	"net/http"
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
// secret は鍵素材であり、外部パッケージにそのまま露出させないため非公開フィールドにする
type JWTVerifier struct {
	Issuer   string
	Audience string
	secret   []byte
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
	return &JWTVerifier{Issuer: c.Issuer, Audience: c.Audience, secret: []byte(v), cacheTTL: ttl}, nil
}

// Handler 次の Handler を JWT 検証で包む
// /healthz はスキップする
// 現状の MVP では実 JWT 検証ロジックが未実装のため、有効化時は fail-closed で
// 503 Service Unavailable を返す。実装は go-jwt と JWKS fetch を統合する将来
// フェーズで完成させる
func (j *JWTVerifier) Handler(next http.Handler) http.Handler {
	if j == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		// 実 JWT 検証が未配線のため fail-closed で拒否する。Bearer Token (04 番) を
		// 併用しているなら BearerAuth が先に通すため、ここに到達するのは
		// eyJ で始まる JWT 形式の値を投入された場合のみとなる
		http.Error(w, "JWT verification not implemented (server-side); enable OAuth2 only after JWKS integration is configured", http.StatusServiceUnavailable)
	})
}
