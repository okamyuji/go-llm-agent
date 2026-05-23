package httpapi

import (
	"errors"
	"fmt"
	"os"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// buildMiddleware config からミドルウェア群を構築する
// secret_env からトークンを解決し、解決不能なエントリはエラーで弾く
func buildMiddleware(cfg *config.Config) (*BearerAuth, *TokenBucketLimiter, *AllowlistCIDR, *CORS, error) {
	var auth *BearerAuth
	if cfg.Server.Auth.Enabled {
		tokens, err := resolveBearerTokens(cfg.Server.Auth.BearerTokens)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		auth = NewBearerAuth(tokens)
	}
	var limiter *TokenBucketLimiter
	if cfg.Server.RateLimit.Enabled {
		limiter = NewTokenBucketLimiter(cfg.Server.RateLimit.RPS, cfg.Server.RateLimit.Burst, cfg.Server.RateLimit.PerToken)
	}
	allowlist, err := NewAllowlistCIDR(cfg.Server.Allowlist.CIDRs, cfg.Server.Allowlist.TrustedProxies...)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("invalid allowlist cidr: %w", err)
	}
	cors := NewCORS(cfg.Server.CORS.Enabled, cfg.Server.CORS.AllowOrigins, cfg.Server.CORS.AllowMethods, cfg.Server.CORS.AllowHeaders)
	return auth, limiter, allowlist, cors, nil
}

// resolveBearerTokens 設定されたエントリの secret_env を環境変数から解決する
// 値の直書きは未対応で secret_env が空または未設定の値だとエラー
// 異なる ID で同一トークン値が解決された場合は、上書きによる権限混乱を防ぐためエラーで弾く
func resolveBearerTokens(entries []config.ServerBearerToken) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range entries {
		if e.SecretEnv == "" {
			return nil, errors.New("bearer_tokens entry must have secret_env (direct value is not allowed)")
		}
		v, ok := os.LookupEnv(e.SecretEnv)
		if !ok || v == "" {
			return nil, fmt.Errorf("bearer token env %q is not set", e.SecretEnv)
		}
		if existing, dup := out[v]; dup {
			return nil, fmt.Errorf("bearer token env %q resolves to a value already used by id %q (set distinct secret values per id)", e.SecretEnv, existing)
		}
		out[v] = e.ID
	}
	return out, nil
}
