# 04. HTTP API の Bearer 認証とレート制限と CORS

## 1. 概要

`serve` サブコマンドが公開する `/v1/*` エンドポイントに Bearer トークン認証、トークン別レート制限、CORS、シンプルな IP allowlist を導入します。

## 2. 書籍根拠

Ch12 「Protections from External Threats」では rate limiting、OAuth 2.0、API keys、mTLS が外部脅威対策として列挙されています。本書ではまず最低限の Bearer 認証とレート制限を実装し、mTLS と OAuth2 は 15 番の設計書で扱います。

## 3. 現状分析

`internal/transport/httpapi/server.go` は `/v1/chat/completions`、`/v1/models`、`/healthz` のみで認証なしです。`grep -nE "auth|rate|throttle|middleware" internal/transport/httpapi` は空でした。

## 4. ゴール

- `Authorization: Bearer <token>` を必須化できます。
- 設定されたトークンごとに毎秒リクエスト数を制限できます。
- 簡易 IP allowlist を設定できます。
- CORS ヘッダを設定で許可オリジンに合わせて返します。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/transport/httpapi/middleware.go` を新設します。ミドルウェアは関数チェインで適用し、`/healthz` 以外のすべてのルートを通します。

### 5.2 設定スキーマ

```yaml
server:
  addr: 127.0.0.1:14000
  auth:
    enabled: true
    bearer_tokens:
      - id: local
        secret_env: AGENT_LOCAL_TOKEN
  rate_limit:
    enabled: true
    rps: 5
    burst: 10
    per_token: true
  allowlist:
    cidrs:
      - 127.0.0.1/32
  cors:
    enabled: false
    allow_origins: []
    allow_methods: [GET, POST, OPTIONS]
    allow_headers: [Authorization, Content-Type]
```

### 5.3 公開インターフェース

```go
type BearerAuth struct {
    Tokens map[string]string
}

func (a *BearerAuth) Handler(next http.Handler) http.Handler

type TokenBucketLimiter struct {
    RPS float64
    Burst int
    PerToken bool
}

func (l *TokenBucketLimiter) Handler(next http.Handler) http.Handler

type AllowlistCIDR struct { Nets []*net.IPNet }
func (a *AllowlistCIDR) Handler(next http.Handler) http.Handler
```

### 5.4 データフロー

1. main.cmdServe は config.Server.Auth.BearerTokens の secret_env を解決し、`Tokens` map を構築します。
2. ServeMux の前段に Allowlist → Auth → RateLimit → CORS の順でミドルウェアを巻きます。
3. Auth は失敗時に 401 を返し、RateLimit は失敗時に 429 を返します。
4. `/healthz` だけは Auth と RateLimit を通過させます。
5. 15 番の OAuth2 機能が有効化されている場合、BearerAuth は受け取った値が `eyJ` で始まるとき自身では認証判定を行わず、後段の `JWTVerifier` ミドルウェアに委譲します。これにより Bearer Token（任意文字列の事前共有秘密）と OAuth2 JWT を同じ `Authorization: Bearer <value>` ヘッダで併用できます。`eyJ` 接頭辞の判定は事前共有 Bearer 値の先頭文字がそれと衝突しないよう、設定時にバリデーションします。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 4.1 | RED | bearer なしで `/v1/models` が 401 を返すテスト |
| 4.2 | GREEN | BearerAuth ミドルウェアを実装 |
| 4.3 | RED | RPS 1 で 3 回連続呼び出して 1 件が 429 になるテスト |
| 4.4 | GREEN | golang.org/x/time/rate でリミッタを実装 |
| 4.5 | RED | 192.0.2.1 からのアクセスが 403 になるテスト |
| 4.6 | GREEN | Allowlist ミドルウェアを実装 |
| 4.7 | RED | CORS preflight に 204 が返るテスト |
| 4.8 | GREEN | CORS ミドルウェアを実装 |
| 4.9 | REFACTOR | E2E と README 反映 |

## 7. テスト計画

### 7.1 ユニット

- 設定オフ時にミドルウェアが完全に no-op であること。
- Token が誤っているときに 401 が返ること。
- Burst を使い切ったあと 429 が返ること。

### 7.2 統合

- `internal/transport/httpapi/server_test.go` を拡張し、認証ありで chat と models が動くこと。
- 認証 OFF 設定で動かしたとき 旧挙動と互換であること。

### 7.3 E2E

`tests/e2e/04-http-auth.sh` を作成し、`AGENT_LOCAL_TOKEN` を `.env` 風に与えてサーバ起動、`curl` で 401/200/429 のシナリオを確認します。

## 8. ロールアウト

`server.auth.enabled` の既定値は false で旧挙動互換です。CI でも既定 false で動かします。README に有効化手順を載せます。

## 9. リスクと対策

- ローカル運用で AGENT_LOCAL_TOKEN を平文に書く事故を防ぐため secret_env 指定だけ受け付け、values の直書きは拒否します。
- per_token=true のレートリミッタは map のサイズが膨らみすぎないように LRU で管理します。

## 10. 完了基準

- 認証 OFF の既存テストが落ちないこと。
- `tests/e2e/04-http-auth.sh` が成功すること。
- README の Security 節に Bearer Auth と Rate Limit を追記すること。
