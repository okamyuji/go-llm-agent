//go:build windows

package cliui

import "os"

// beginPromptMode Windows コンソールには pty のカノニカルバッファ制限が無いため、
// 従来どおり cooked のまま読む (echo と行編集はコンソールに任せ、自前 echo は使わない)。
func beginPromptMode(_ *os.File) (restore func(), ok bool) {
	return func() {}, false
}
