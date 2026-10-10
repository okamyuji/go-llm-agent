# 画像入力

## 概要

`agent chat`の`/image <path> <質問>`と`agent run -image <path> -p <質問>`は、画像を1枚添付してLLMへ送ります。送れるproviderはllamacppとopenaiです。

## 内部表現

`llm.Message`は`Images []llm.Image`を持ちます。`llm.Image`はパス（`Name`）、MIMEタイプ、本体のバイト列を持ちます。

本文は`Content string`のまま残しました。本文をpartsの配列に置き換えると、全providerとすべての呼び出し側を書き換えることになります。画像を扱わない経路は`Content`だけを読み続ければよく、変更はllamacppとopenaiの送信部分に閉じます。

## provider ごとの扱い

llamacppとopenaiは、画像を持つメッセージだけ`content`をOpenAI互換のpartsの配列にします。配列は本文のtext partと、画像ごとのimage_url part（data URI）から成ります。画像を持たないメッセージは従来どおり文字列で送ります。

anthropic、gemini、ollamaは、画像を持つメッセージを受け取るとHTTPを呼ぶ前に`llm.ErrImagesUnsupported`を返します。画像を黙って落とすと、モデルは見ていない画像について答えます。

llama-serverで画像を読むには、モデルに対応する`--mmproj`の指定が要ります。

## 入力の検証

`llm.LoadImage`は次の条件を満たさないファイルを拒否し、LLMを呼びません。

- 形式は`http.DetectContentType`で中身から判定し、PNG、JPEG、GIF、WebPだけを受け付けます
- 上限は20MB（`llm.MaxImageBytes`）です。履歴の画像は毎ターンbase64で送り直すため、メモリと送信量を抑える値にしています
- 空のファイルとディレクトリは拒否します

`agent run`は設定とproviderを組み立てる前に画像を読みます。

## 会話履歴とセッション記録

添付した画像は会話履歴に残り、以後のターンでも毎回送ります。llama-server（Saluki 27B、768×768のPNG、`-np 1`）で測ると、2ターン目は画像を含む637トークンをprompt cacheから再利用し、1.2秒で正しく答えました。画像を履歴から落とすと先頭が変わってcacheが効かず、2.5秒かかったうえに画像の細部を誤答しました。このため、送り直しを続けています。費用は、毎ターンのリクエスト本文（この画像で約1.7MB）と、画像1枚あたり数百トークンのコンテキストです。

画像が履歴に残っている間に`/model`で非対応providerへ切り替えると、画像を付けていないターンも`images not supported`で失敗します。`/clear`で履歴を捨てると元に戻ります。

セッションのJSONLには画像の本体を保存しません。base64で保存すると1枚で最大約27MB増えるためです。代わりにuserメッセージの本文の前に`[画像: <path>]`の行を残します。`-resume`で再開した会話には画像が含まれず、この行と過去の応答だけが残ります。

`/compact`の要約と入力スキャナは本文だけを読みます。圧縮した区間の画像は消え、要約のテキストだけが残ります。
