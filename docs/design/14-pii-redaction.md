# 14. 出力 PII 自動マスキング

## 1. 概要

agent 出力テキスト、監査ログ、トレース属性、session 永続化のすべての経路で PII（個人情報）と機微情報のパターンを統一マスキングします。

## 2. 書籍根拠

Ch12「Handling Sensitive Data」「data minimization」を参照します。書籍は pseudonymization と anonymization を組み合わせ、ログ・キャッシュ・中間出力にも適用すべきと述べています。

## 3. 現状分析

`internal/secret/mask.go` は env キー名サフィックスのみ対応です。本文中のメールアドレスや電話番号、マイナンバー、クレジットカード番号、IPv4 は素通しです。

## 4. ゴール

- PII パターンを設定で定義し、複数経路で同じ Redactor が動きます。
- 06 番設計書の `safety.output_redactor` と統合して使えます。
- 日本語の電話番号やマイナンバーパターンを含みます。
- マスキング結果はトレース属性、ログ、SSE、session 永続化のいずれも同じ文字列です。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/safety/pii.go` を追加し、06 番で導入した Redactor インターフェースを extend する形で PII ルールを束ねます。Redactor は configuration から動的にルールを登録できます。

### 5.2 設定スキーマ

```yaml
safety:
  pii_redactor:
    enabled: true
    rules:
      - id: email
        regex: "[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}"
        replacement: "[REDACTED:EMAIL]"
      - id: jp_phone
        # ハイフンあり/なし双方を許容し、国際表記 +81 にも対応する
        regex: "(?:\\+?81[- ]?|0)\\d{1,4}[- ]?\\d{1,4}[- ]?\\d{3,4}"
        replacement: "[REDACTED:JP_PHONE]"
      - id: jp_mynumber
        regex: "\\b\\d{4}\\s?\\d{4}\\s?\\d{4}\\b"
        replacement: "[REDACTED:JP_MYNUMBER]"
      - id: ipv4
        # 0-255 のオクテット範囲を厳密にチェックして version 番号などへの誤マッチを防ぐ
        regex: "\\b(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)(\\.(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)){3}\\b"
        replacement: "[REDACTED:IPV4]"
```

上記は設計書の参考例です。実装側 (`config.yaml.example` と `cmd/agent/main.go`) では同じパターンを採用し、ユニットテストで偽陽性 (例: `192.300.1.1` / `09011112222a` のような部分文字列) を弾けることを確認します。

### 5.3 公開インターフェース

`internal/safety/pii.go`:

```go
// PIIRedactor 06 番の Redactor インターフェースを実装する PII 専用 Redactor
// agent.Service は ChainRedactor(outputRedactor, piiRedactor) を 1 つの Redactor として扱う
type PIIRedactor struct{ rules []Rule }

// NewPIIRedactor 設定から PIIRedactor を構築する
func NewPIIRedactor(rules []Rule) (*PIIRedactor, error)

// Redact 06 番の Redactor インターフェースを満たすメソッド
func (r *PIIRedactor) Redact(text string) string
```

agent.Service の初期化時に `safety.ChainRedactor(outputRedactor, piiRedactor)` を 1 つの `Redactor` として組み立て、Event の DeltaText、Final.Content、session.Append、span 属性、slog のすべての出口で同じ Chain を通します。

`internal/obs/log.go` に PII Redactor を組み込む `WrapLogger(logger, redactor)` を追加します。

### 5.4 データフロー

1. agent.Service の Event ストリーム経由でクライアントに返るテキストは `PIIRedactor.Redact` を通過します。
2. session.Append でも同じ Redactor を通します。
3. OTel span 属性に文字列を載せる場所も Redactor を通します。
4. slog のハンドラを `WrapLogger` で Redactor 適用済みにします。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 14.1 | RED | PIIRedactor の各パターンテスト |
| 14.2 | GREEN | pii.go を実装 |
| 14.3 | RED | session.Append の通過テスト |
| 14.4 | GREEN | session.go を改修 |
| 14.5 | RED | OTel 属性の redact テスト |
| 14.6 | GREEN | obs パッケージに hook を追加 |
| 14.7 | REFACTOR | E2E `tests/e2e/14-pii-redact.sh` |

## 7. テスト計画

### 7.1 ユニット

- 日本語電話番号、マイナンバー、メール、IPv4 がそれぞれマスクされること。
- 設定 OFF で全て素通しになること。

### 7.2 統合

- chat で「私のメールは a@example.com です」と話したら、session JSONL と SSE 出力の両方に `[REDACTED:EMAIL]` が反映されること。

### 7.3 E2E

`tests/e2e/14-pii-redact.sh` で run コマンドの stdout と session ファイル両方を grep し、原文の email が残っていないことを確認します。

## 8. ロールアウト

既定 on とし、必要な場合のみ disable します。06 番の安全フィルタと衝突しないよう、PII Redactor は output_redactor の後段で動きます。

## 9. リスクと対策

- IPv4 の正規表現が version 番号などを誤マッチする可能性があります。文字境界 `\b` を必ず付け、テストで誤マッチを抑止します。
- JP phone 正規表現は将来の電話番号体系変更に脆いため、設定で上書き可能にします。

## 10. 完了基準

- pii テストが網羅。
- E2E 成功。
- README の Security 節に PII Redaction を追記します。
