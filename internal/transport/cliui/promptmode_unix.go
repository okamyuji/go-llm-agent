//go:build !windows

package cliui

import (
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// beginPromptMode は端末をプロンプト読み取り用の非カノニカルモードにし、元へ戻す関数を返す。
// 端末でなければ no-op。カノニカル (cooked) のままだと 1 行あたり MAX_CANON (1024 バイト)
// を超える貼り付けでカーネルの入力キューが詰まり、REPL 全体がハングするため、
// 行の組み立て・編集・echo は readPrompt が自前で行う。
// raw (term.MakeRaw) と違い ISIG は残すので、Ctrl-C の SIGINT → signal.NotifyContext →
// ctx キャンセルによる終了経路は従来どおり機能する。出力フラグも触らない (\n → \r\n 変換維持)。
func beginPromptMode(f *os.File) (restore func(), ok bool) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, false
	}
	saved, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return func() {}, false
	}
	tio := *saved
	// ICANON: 行バッファリングと行編集 (ERASE/KILL) を無効化 → 1024 バイト制限を回避
	// ECHO 系: カーネル echo を止め、readPrompt の自前 echo に置き換える
	tio.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ECHOK | unix.ECHONL | unix.ECHOCTL | unix.ECHOKE | unix.ECHOPRT
	// 非カノニカルでは VMIN/VTIME が VEOF/VEOL と同じスロットを共有するため明示設定する
	tio.Cc[unix.VMIN] = 1
	tio.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &tio); err != nil {
		return func() {}, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, saved) }, true
}
