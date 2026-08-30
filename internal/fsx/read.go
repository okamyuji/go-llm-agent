package fsx

import (
	"io"
	"os"
	"unicode/utf8"
)

// ReadCapped path をシンボリックリンクを辿らずに開き、maxBytes > 0 のときは
// 先頭 maxBytes バイト (rune 境界で切り詰め) だけを返す。maxBytes <= 0 は全文を読む。
// 読み取りは maxBytes+1 バイトで打ち切り、ディスク上の巨大ファイルを丸ごと
// メモリへ載せない
func ReadCapped(path string, maxBytes int) (string, error) {
	f, err := OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if maxBytes > 0 {
		r = io.LimitReader(f, int64(maxBytes)+1)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return TruncateAtRuneBoundary(string(b), maxBytes), nil
}

// TruncateAtRuneBoundary s が maxBytes バイトを超える場合、maxBytes バイト目の
// 直前にある rune 境界までを返す。maxBytes <= 0 または超過しない場合はそのまま返す。
// 末尾の不完全なシーケンスだけを削る。utf8.ValidString で全体を見ながら削る方式だと、
// 内容の途中に不正バイトがあるときそこまで大半が消えるため採らない
func TruncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
