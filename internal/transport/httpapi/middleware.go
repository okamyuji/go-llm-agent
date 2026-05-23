package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// healthPath 認証とレート制限をスキップする公開エンドポイント
const healthPath = "/healthz"

// BearerAuth Bearer Token 認証ミドルウェア
// Tokens は token 値そのものをキーに ID を値として保持する
type BearerAuth struct {
	Tokens         map[string]string
	DeferJWTPrefix string
}

// NewBearerAuth secret_env 解決済みトークンマップから BearerAuth を構築する
// deferJWTPrefix で始まる値は 15 番設計書の JWTVerifier に委譲する想定で素通しする
func NewBearerAuth(tokens map[string]string, deferJWTPrefix string) *BearerAuth {
	return &BearerAuth{Tokens: tokens, DeferJWTPrefix: deferJWTPrefix}
}

// Handler 次の Handler を Bearer Auth で保護したラッパを返す
// Tokens が空のとき認証は無効として扱う
func (a *BearerAuth) Handler(next http.Handler) http.Handler {
	if a == nil || len(a.Tokens) == 0 {
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
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		value := strings.TrimPrefix(auth, prefix)
		if a.DeferJWTPrefix != "" && strings.HasPrefix(value, a.DeferJWTPrefix) {
			// 15 番の JWT 検証ミドルウェアに委譲するためここでは通過させる
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.Tokens[value]; !ok {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TokenBucketLimiter 全体またはトークン別の RPS 制限を提供する
type TokenBucketLimiter struct {
	RPS      float64
	Burst    int
	PerToken bool

	mu       sync.Mutex
	global   *rate.Limiter
	perToken map[string]*rate.Limiter
}

// NewTokenBucketLimiter 設定値からレートリミッタを構築する
// RPS<=0 か Burst<=0 ではミドルウェアは適用されない（nil-safe）
func NewTokenBucketLimiter(rps float64, burst int, perToken bool) *TokenBucketLimiter {
	if rps <= 0 || burst <= 0 {
		return nil
	}
	return &TokenBucketLimiter{
		RPS:      rps,
		Burst:    burst,
		PerToken: perToken,
		perToken: map[string]*rate.Limiter{},
	}
}

// Handler 次の Handler を レート制限で包む
func (l *TokenBucketLimiter) Handler(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(r) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow リクエストが許可されるか判定する
// per_token=true のとき Bearer Token が無いリクエストは r.RemoteAddr ではなく
// 専用バケットに集約して NAT 経由のレート上限バイパスを防ぐ
func (l *TokenBucketLimiter) allow(r *http.Request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.PerToken {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = "__anonymous__"
		}
		lim, ok := l.perToken[key]
		if !ok {
			lim = rate.NewLimiter(rate.Limit(l.RPS), l.Burst)
			l.perToken[key] = lim
		}
		return lim.Allow()
	}
	if l.global == nil {
		l.global = rate.NewLimiter(rate.Limit(l.RPS), l.Burst)
	}
	return l.global.Allow()
}

// AllowlistCIDR IP allowlist ミドルウェア
type AllowlistCIDR struct {
	Nets []*net.IPNet
}

// NewAllowlistCIDR CIDR 文字列のリストから AllowlistCIDR を構築する
// 文字列が空または全てパースに失敗した場合は nil を返し、ミドルウェアは適用されない
func NewAllowlistCIDR(cidrs []string) (*AllowlistCIDR, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		nets = append(nets, n)
	}
	return &AllowlistCIDR{Nets: nets}, nil
}

// Handler 次の Handler を IP allowlist で包む
func (a *AllowlistCIDR) Handler(next http.Handler) http.Handler {
	if a == nil || len(a.Nets) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil {
			http.Error(w, "invalid remote address", http.StatusForbidden)
			return
		}
		for _, n := range a.Nets {
			if n.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "client ip not allowed", http.StatusForbidden)
	})
}

// CORS 設定可能な CORS ヘッダミドルウェア
type CORS struct {
	AllowOrigins []string
	AllowMethods []string
	AllowHeaders []string
}

// NewCORS 設定から CORS を構築する。Enabled=false なら nil
func NewCORS(enabled bool, origins, methods, headers []string) *CORS {
	if !enabled {
		return nil
	}
	return &CORS{AllowOrigins: origins, AllowMethods: methods, AllowHeaders: headers}
}

// Handler 次の Handler を CORS ヘッダで包む。OPTIONS は 204 で返す
func (c *CORS) Handler(next http.Handler) http.Handler {
	if c == nil {
		return next
	}
	originSet := map[string]struct{}{}
	for _, o := range c.AllowOrigins {
		originSet[o] = struct{}{}
	}
	_, hasWildcard := originSet["*"]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := originSet[origin]; ok {
				// 明示許可された Origin は echo して credential 互換を保つ
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else if hasWildcard {
				// "*" 指定時のみ wildcard を返す。未許可の Origin を勝手に echo しない
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
		}
		if len(c.AllowMethods) > 0 {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(c.AllowMethods, ", "))
		}
		if len(c.AllowHeaders) > 0 {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(c.AllowHeaders, ", "))
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
