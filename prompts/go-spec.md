## Go 言語仕様リファレンス (公式言語仕様書より抜粋)

### 型システム
- 基本型: bool, string, int/int8/int16/int32/int64, uint系, float32/float64, complex64/complex128, byte(=uint8), rune(=int32)。
- 複合型: array, slice, map, struct, pointer, function, interface, channel。
- ゼロ値: 数値は0、boolはfalse、stringは""、ポインタ・スライス・map・channel・interface・functionはnil。
- 型変換: 明示的な変換が必要。暗黙変換はない(intとint64も別型)。

### 変数と宣言
- var宣言: `var x int` はゼロ値で初期化。
- 短縮宣言: `x := 10` は関数内でのみ使用可能。
- 定数: `const`で宣言。コンパイル時に値が決まる。iota で連番生成。
- ブランク識別子: `_` は値を捨てる。

### 関数とメソッド
- 関数: 第一級オブジェクト。変数に代入可能。
- メソッド: レシーバ付き関数。値レシーバとポインタレシーバがある。
- 値レシーバ: レシーバのコピーに対して操作。元のオブジェクトは変更されない。
- ポインタレシーバ: レシーバ本体に対して操作。元のオブジェクトを変更できる。
- 可変長引数: `func f(args ...int)` で宣言。呼び出し側では `f(1, 2, 3)` またはスライス展開 `f(s...)`。
- 複数戻り値: `func f() (int, error)` のように複数の値を返せる。
- 名前付き戻り値: `func f() (result int, err error)` で宣言。裸のreturnで返せる。

### インターフェース
- インターフェースはメソッドセットを定義する型。
- 暗黙の実装: implements宣言なしでメソッドセットを満たせば実装とみなされる。
- 空インターフェース `interface{}` / `any`: 全ての型を受け入れる。
- インターフェース値は内部的に(動的型, 動的値)のペアで表現される。
- インターフェース値がnilになるのは動的型と動的値の両方が未設定の場合のみ。
- 型アサーション: `v, ok := i.(T)` でインターフェースから具体型を取り出す。
- 型switch: `switch v := i.(type) { case T: ... }` で型に応じた処理を分岐。

### スライスとマップ
- スライス: 配列への参照。長さ(len)と容量(cap)を持つ。
- `var s []int`: nil スライス。`s := []int{}`: 空スライス(非nil)。`make([]int, 0)`: 空スライス(非nil)。
- append: スライスに要素を追加。容量を超えると新しい配列が確保される。
- copy: スライス間で要素をコピー。
- マップ: `make(map[K]V)` で初期化。nilマップへの書き込みはpanicする。
- マップのイテレーション順序は非決定的(毎回異なりうる)。
- マップのキーには比較可能な型のみ使用可能(スライスは不可)。

### 制御構造
- if/else: 条件分岐。初期化文を含められる `if err := f(); err != nil { ... }`。
- for: 唯一のループ構文。`for {}`, `for cond {}`, `for i := 0; i < n; i++ {}`, `for k, v := range m {}` の4形式。
- switch: 式switch(値の比較)と型switch(インターフェースの型判定)がある。fallthrough不要(Cと異なる)。
- select: 複数のチャネル操作を待つ。readyなものからランダムに1つ選ばれる。

### defer文
- defer文で指定した関数は、囲む関数がreturn文の実行、関数本体の末尾到達、panicのいずれかで戻る直前に呼ばれる。
- 複数のdeferはLIFO(後入れ先出し)順で実行される。
- deferの引数は、defer文の評価時に即座に評価され保存される(遅延評価ではない)。
- defer内でnamed return valueを変更できる。

### エラー処理
- errorインターフェース: `Error() string` メソッドを持つ。
- 慣例的にerrorは最後の戻り値。`if err != nil { return err }` パターン。
- errors.Is: エラーチェーンを辿って一致を確認。
- errors.As: エラーチェーンから特定の型のエラーを取得。
- fmt.Errorf("%w", err): エラーをラップ。

### 並行処理
- goroutine: `go f()` で軽量スレッドを起動。
- channel: goroutine間の通信。`make(chan T)` でバッファなし、`make(chan T, n)` でバッファ付き。
- バッファなしチャネル: 送信側は受信側が読み取るまでブロック。
- close: チャネルを閉じる。閉じたチャネルからの受信はゼロ値を返す。
- sync.WaitGroup: 複数のgoroutineの完了を待つ。Add/Done/Wait。
- sync.Mutex: 排他制御。Lock/Unlock。
- sync.RWMutex: 読み取りは並行可、書き込みは排他。
- データ競合: 同じ変数に対する非同期の読み書きは未定義動作。-race フラグで検出。

### パッケージとインポート
- パッケージ: ディレクトリ単位。同一ディレクトリ内は同一パッケージ名。
- エクスポート: 大文字で始まる識別子のみ外部パッケージからアクセス可能。
- init関数: パッケージ初期化時に自動実行。複数定義可能。
- 循環インポートは禁止。

### テスト
- `_test.go` ファイルに `Test*` 関数を定義。
- `go test -race ./...`: データ競合検出付きテスト。
- `go test -cover`: カバレッジ計測。
- テーブル駆動テスト: `[]struct{ name string; ... }` のスライスでケースを定義。
- ベンチマーク: `Benchmark*` 関数。`testing.B` を使用。
