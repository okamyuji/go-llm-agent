package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// healthPath 認証とレート制限をスキップする公開エンドポイント
const healthPath = "/healthz"

// BearerAuth Bearer Token 認証ミドルウェア
// digests は token 値の SHA-256 ハッシュをキーに ID を値として保持する
// 平文を保持しないことで生トークン長の漏洩を避け、lookup の長さ比較を固定長 (32 バイト) に統一する
type BearerAuth struct {
	digests map[[sha256.Size]byte]string
}

// NewBearerAuth secret_env 解決済みトークンマップから BearerAuth を構築する
// 構築時に SHA-256 ハッシュを取り、平文トークンはメモリに残さない
// digests は構築後に変更されない
func NewBearerAuth(tokens map[string]string) *BearerAuth {
	digests := make(map[[sha256.Size]byte]string, len(tokens))
	for tok, id := range tokens {
		digests[sha256.Sum256([]byte(tok))] = id
	}
	return &BearerAuth{digests: digests}
}

// Handler 次の Handler を Bearer Auth で保護したラッパを返す
// digests が空のとき認証は無効として扱う
// 検証は SHA-256 ダイジェストを crypto/subtle.ConstantTimeCompare で比較するため、
// 平文長の比較を必要とせず、タイミングサイドチャネルとトークン長漏洩の両方を抑える
func (a *BearerAuth) Handler(next http.Handler) http.Handler {
	if a == nil || len(a.digests) == 0 {
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
		if !a.lookup(value) {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// lookup 入力 token を SHA-256 でハッシュし、固定長ダイジェスト同士を ConstantTimeCompare で比較する
// 平文長の差で漏れる情報をゼロにすると同時にタイミングサイドチャネルも抑える
func (a *BearerAuth) lookup(value string) bool {
	d := sha256.Sum256([]byte(value))
	matched := 0
	for stored := range a.digests {
		// stored を addressable にするため一旦ローカルへ取り、スライス化して比較する
		s := stored
		if subtle.ConstantTimeCompare(s[:], d[:]) == 1 {
			matched = 1
		}
	}
	return matched == 1
}

// TokenBucketLimiter 全体またはトークン別の RPS 制限を提供する
// perTokenMaxEntries per-token rate limiter キャッシュの最大エントリ数
// これを超えると最終利用時刻が最も古いエントリから順に追い出して DoS リスクを抑える
const perTokenMaxEntries = 1024

// tokenLimiterEntry per-token バケットと最終利用時刻のペア
type tokenLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// TokenBucketLimiter 全体またはトークン別の RPS 制限を提供する
// per-token キャッシュは perTokenMaxEntries で上限を設けて DoS リスクを抑える
type TokenBucketLimiter struct {
	RPS      float64
	Burst    int
	PerToken bool

	mu       sync.Mutex
	global   *rate.Limiter
	perToken map[string]*tokenLimiterEntry
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
		perToken: map[string]*tokenLimiterEntry{},
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
			// RFC 6585 §4 と OWASP API Security Best Practices に従い Retry-After を返す
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow リクエストが許可されるか判定する
// per_token=true のとき Bearer Token が無いリクエストは r.RemoteAddr ではなく
// 専用バケットに集約して NAT 経由のレート上限バイパスを防ぐ
// perToken キャッシュは perTokenMaxEntries で上限し、最終利用時刻の古い順に追い出す
func (l *TokenBucketLimiter) allow(r *http.Request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.PerToken {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = "__anonymous__"
		}
		now := time.Now()
		ent, ok := l.perToken[key]
		if !ok {
			if len(l.perToken) >= perTokenMaxEntries {
				l.evictOldest()
			}
			ent = &tokenLimiterEntry{limiter: rate.NewLimiter(rate.Limit(l.RPS), l.Burst), lastUsed: now}
			l.perToken[key] = ent
		} else {
			ent.lastUsed = now
		}
		return ent.limiter.Allow()
	}
	if l.global == nil {
		l.global = rate.NewLimiter(rate.Limit(l.RPS), l.Burst)
	}
	return l.global.Allow()
}

// evictOldest perToken マップから最終利用時刻が最も古いエントリを 1 件削除する
// 呼び出し側で l.mu を保持していることが前提
func (l *TokenBucketLimiter) evictOldest() {
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range l.perToken {
		if first || e.lastUsed.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.lastUsed
			first = false
		}
	}
	if oldestKey != "" {
		delete(l.perToken, oldestKey)
	}
}

// AllowlistCIDR IP allowlist ミドルウェア
// Nets はクライアント IP の許可リスト
// TrustedProxies は X-Forwarded-For / X-Real-IP を信頼する直接接続元の CIDR
// 設定が空のときヘッダを無視して r.RemoteAddr のみで判定する fail-safe 設計とする
type AllowlistCIDR struct {
	Nets           []*net.IPNet
	TrustedProxies []*net.IPNet
}

// NewAllowlistCIDR CIDR 文字列のリストから AllowlistCIDR を構築する
// 文字列が空または全てパースに失敗した場合は nil を返し、ミドルウェアは適用されない
// trustedProxies は X-Forwarded-For / X-Real-IP を信頼する直接接続元の CIDR
// (リバースプロキシ運用時に設定する。指定が無ければヘッダは無視され r.RemoteAddr のみで判定する)
func NewAllowlistCIDR(cidrs []string, trustedProxies ...string) (*AllowlistCIDR, error) {
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
	var trusted []*net.IPNet
	for _, c := range trustedProxies {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy cidr: %w", err)
		}
		trusted = append(trusted, n)
	}
	return &AllowlistCIDR{Nets: nets, TrustedProxies: trusted}, nil
}

// Handler 次の Handler を IP allowlist で包む
// TrustedProxies が設定されている場合、その経由のリクエストのみ X-Forwarded-For / X-Real-IP の
// 左端 IP を信頼してクライアント IP として扱う
// 信頼しない経路ではヘッダを無視し、直接接続元の r.RemoteAddr のみで判定する
func (a *AllowlistCIDR) Handler(next http.Handler) http.Handler {
	if a == nil || len(a.Nets) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, a.TrustedProxies)
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

// clientIP r.RemoteAddr を基準にクライアント IP を判定する
// 直接接続元が trustedProxies に含まれる場合だけ X-Forwarded-For / X-Real-IP の左端を採用する
func clientIP(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return nil
	}
	if len(trustedProxies) == 0 {
		return peer
	}
	trusted := false
	for _, n := range trustedProxies {
		if n.Contains(peer) {
			trusted = true
			break
		}
	}
	if !trusted {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 左端 IP を採用する
		// この経路は直接接続元 (peer) が trustedProxies に含まれている場合のみ実行され、
		// 信頼境界の外から流入する未検証 XFF をそのまま使うことはない
		// よって悪意ある XFF spoof の前提として「信頼プロキシそのものが嘘の XFF を送る」が必要となり、
		// それは TrustedProxies 設定の運用上のミスに帰着する
		left := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if ip := net.ParseIP(left); ip != nil {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil {
			return ip
		}
	}
	return peer
}

// CORS 設定可能な CORS ヘッダミドルウェア
// originSet と hasWildcard を NewCORS で 1 度だけ計算し、Handler 内のリクエストごとの再計算を避ける
type CORS struct {
	AllowOrigins []string
	AllowMethods []string
	AllowHeaders []string

	originSet   map[string]struct{}
	hasWildcard bool
}

// NewCORS 設定から CORS を構築する。Enabled=false なら nil
func NewCORS(enabled bool, origins, methods, headers []string) *CORS {
	if !enabled {
		return nil
	}
	originSet := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		originSet[o] = struct{}{}
	}
	_, hasWildcard := originSet["*"]
	return &CORS{
		AllowOrigins: origins,
		AllowMethods: methods,
		AllowHeaders: headers,
		originSet:    originSet,
		hasWildcard:  hasWildcard,
	}
}

// Handler 次の Handler を CORS ヘッダで包む。OPTIONS は 204 で返す
func (c *CORS) Handler(next http.Handler) http.Handler {
	if c == nil {
		return next
	}
	originSet := c.originSet
	hasWildcard := c.hasWildcard
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := originSet[origin]; ok {
				// 明示許可された Origin は echo して credential 互換を保つ
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// 動的に Origin を echo する場合は CDN キャッシュ汚染を避けるため必ず Vary を付ける
				w.Header().Add("Vary", "Origin")
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
