# 15. mTLS と OAuth2 リソースサーバ対応

## 1. 概要

`serve` サブコマンドの HTTP サーバを TLS 終端できるようにし、mTLS でクライアント証明書を検証、または OAuth2 リソースサーバとして JWT を検証する経路を追加します。

## 2. 書籍根拠

Ch12「mutual TLS (mTLS) authentication」「OAuth 2.0 or API keys」を参照します。多テナントや法人 IdP 連携を想定した運用に必須の機能です。

## 3. 現状分析

`server.addr` の HTTP plain のみで、TLS、mTLS、OAuth2 のいずれも未実装です。

## 4. ゴール

- `server.tls.cert_file` と `key_file` の指定で TLS 終端が可能になります。
- `server.tls.client_ca_file` の指定で mTLS が有効化されます。
- `server.oauth2` の指定で JWT 検証ミドルウェアが追加されます。
- Bearer Token / OAuth2 / mTLS は設定に応じて有効化できます。複数を同時に有効化した場合の判定は AND (全て成功必須) で固定し、ある方式が成功しても他の方式が拒否したらリクエストは 401 で終端します。
  - 例 (AND): mTLS + OAuth2 の両方を有効化した場合、クライアント証明書と JWT の両方を提示し、両方の検証に成功した場合のみハンドラに到達します。
  - 優先度ベース (OR) の代替評価は MVP ではサポートせず、必要になった時点で別設計で追加します。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/transport/httpapi/tls.go` を新設し、`http.Server.TLSConfig` を構築します。`internal/transport/httpapi/oauth.go` で OAuth2 JWT 検証ミドルウェアを実装します。`github.com/golang-jwt/jwt/v5` を採用します。

### 5.2 設定スキーマ

```yaml
server:
  tls:
    enabled: false
    cert_file: ./certs/server.crt
    key_file: ./certs/server.key
    client_ca_file: ./certs/client_ca.pem
    min_version: "1.3"
  oauth2:
    enabled: false
    issuer: https://idp.example.com/
    audience: go-llm-agent
    jwks_url: https://idp.example.com/.well-known/jwks.json
    cache_ttl_seconds: 300
```

### 5.3 公開インターフェース

```go
type TLSConfig struct{...}

func BuildTLSConfig(c TLSConfig) (*tls.Config, error)

type JWTVerifier struct{...}

func NewJWTVerifier(issuer, audience, jwksURL string, cacheTTL time.Duration) (*JWTVerifier, error)

func (v *JWTVerifier) Handler(next http.Handler) http.Handler
```

### 5.4 データフロー

1. main.cmdServe は TLSConfig が有効な場合 `http.Server.ListenAndServeTLS` を呼びます。
2. mTLS は `tls.Config.ClientCAs` と `ClientAuth=RequireAndVerifyClientCert` で実装します。
3. JWT 検証は jwks_url を fetch し、署名検証と issuer/audience チェックを行います。
4. 04 番の Bearer Auth、OAuth2 JWT、mTLS の評価順は CORS → Allowlist → RateLimit → (有効なものすべて) Auth → mux で固定します。複数の Auth は AND (全て成功必須) で評価し、いずれかが失敗した時点で 401 を返します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 15.1 | RED | BuildTLSConfig のオプション網羅テスト |
| 15.2 | GREEN | tls.go を実装 |
| 15.3 | RED | mTLS なし接続の reject テスト |
| 15.4 | GREEN | ClientAuth を厳密化 |
| 15.5 | RED | JWT 検証の成功/期限切れテスト |
| 15.6 | GREEN | oauth.go を実装 |
| 15.7 | REFACTOR | E2E `tests/e2e/15-mtls.sh` を作成 |

## 7. テスト計画

### 7.1 ユニット

- min_version "1.3" を強制したとき 1.2 接続が reject されること。
- JWT の audience 違いを検出すること。

### 7.2 統合

- `httptest.NewTLSServer` で自己署名証明書を用いたエンドツーエンドテスト。

### 7.3 E2E

`tests/e2e/15-mtls.sh` でテスト用 CA をリポジトリの fixture 配下に生成し（openssl ではなく Go の `crypto/x509` で生成し再現性を保ちます）、agent serve を起動して `curl --cert ... --key ... https://...` で 200 を取得します。

## 8. ロールアウト

既定 OFF で互換性を維持します。`tls.enabled=true` のときは `cert_file` と `key_file` 必須です。`oauth2.enabled=true` のときは `issuer`、`audience`、`jwks_url` 必須です。

## 9. リスクと対策

- 証明書ファイルが誤ってリポジトリにコミットされないよう `.gitignore` と `.gitleaks.toml` を拡張します。
- JWKS の cache を long TTL にすると鍵ローテートに追従できなくなるため、デフォルトを 5 分にします。

## 10. 完了基準

- TLS と mTLS と JWT のユニット/統合テストがすべて通ります。
- E2E が成功。
- README の Security 節に mTLS と OAuth2 の手順を追記します。
