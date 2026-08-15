//go:build darwin

package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// writableDevices literal で許可する実デバイスノード。file-write* は
// file-write-data を含むため、これらを許可しないと >/dev/null へのリダイレクトや
// /dev/tty への直接書き込みが OS 層で拒否される
var writableDevices = []string{"/dev/null", "/dev/tty"}

// writableDeviceSubpaths subpath で許可するデバイスディレクトリ。
// /dev/stdout /dev/stderr は /dev/fd/1 /dev/fd/2 へのシンボリックリンクであり、
// Seatbelt の照合は解決後の実体に対して行われるため literal 指定は効かない
// (darwin 26 で実測: literal "/dev/stdout" では tee /dev/stdout が
// Operation not permitted、subpath "/dev/fd" で成功)
var writableDeviceSubpaths = []string{"/dev/fd"}

// osSandboxPlatformSupported darwin では常に true。
// sandbox-exec の存在有無はここで判定しない (フェイルオープンを避けるため、
// 不在は wrapWithOSSandbox のエラー経路で扱う)
func osSandboxPlatformSupported() bool {
	return true
}

// osSandboxAvailable sandbox-exec が実行可能かを確認する
func osSandboxAvailable() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// buildSeatbeltProfile allowPaths から Seatbelt (SBPL) profile 文字列を生成する
// deny file-write* をデフォルトとし、allowPaths・一時ディレクトリ・書込み可能な
// デバイスファイルへの書き込みのみ許可する。
// 読み取りとネットワークは制限しない (理由は設計書 2 節参照)。
func buildSeatbeltProfile(allowPaths []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	for _, p := range allowPaths {
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", canonicalizeForProfile(p))
	}
	for _, tmp := range temporaryWritablePaths() {
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", tmp)
	}
	for _, dev := range writableDevices {
		fmt.Fprintf(&b, "(allow file-write* (literal %q))\n", dev)
	}
	for _, dir := range writableDeviceSubpaths {
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", dir)
	}
	return b.String()
}

// temporaryWritablePaths OS 一時ディレクトリ群を返す。多くの CLI ツールが
// ビルド・ロック用の一時ファイルをここに作るため、allow_paths とは別枠で常に許可する
func temporaryWritablePaths() []string {
	paths := []string{"/tmp", "/private/tmp"}
	if v := os.Getenv("TMPDIR"); v != "" {
		paths = append(paths, canonicalizeForProfile(v))
	}
	return paths
}

// wrapWithOSSandbox sandbox-exec でコマンドをラップした *exec.Cmd を構築する。
// sandbox-exec が存在しない場合はエラーを返す (フェイルオープン禁止)。
func wrapWithOSSandbox(ctx context.Context, allowPaths []string, name string, args []string) (*exec.Cmd, error) {
	if !osSandboxAvailable() {
		return nil, fmt.Errorf("os_sandbox: sandbox-exec が見つかりません (darwin に標準搭載されているはずです)")
	}
	profile := buildSeatbeltProfile(allowPaths)
	sbArgs := append([]string{"-p", profile, name}, args...)
	return exec.CommandContext(ctx, "sandbox-exec", sbArgs...), nil
}

// canonicalizeForProfile p を SBPL profile へ書く形へ正規化する。
// Abs + Clean のうえで EvalSymlinks を通し、darwin の /tmp -> /private/tmp、
// /var -> /private/var を実体パスへ解決する。解決しないと mktemp -d 由来の
// パスを allow_paths に持つ環境で、許可したはずのパス配下への書込みが
// OS 層で拒否される。
// p 自体が存在しない場合は、存在する最も近い祖先まで遡って解決し、
// 残りの相対部分を結合する (sandbox.go の canonicalize と同じ規則)。
// 祖先まで遡っても解決できない場合は Abs + Clean の結果をそのまま返す
// (profile の生成を失敗させず、少なくともシンボリックリンクを含まない
// 絶対パスとして許可行を書く)。
func canonicalizeForProfile(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	rest := ""
	dir := abs
	for {
		if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs // ルートまで遡っても解決できない
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
