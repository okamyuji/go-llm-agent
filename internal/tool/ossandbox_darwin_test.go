//go:build darwin

package tool

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestBuildSeatbeltProfile_AllowPathsResolvesTmpSymlink(t *testing.T) {
	profile := buildSeatbeltProfile([]string{"/tmp/work"})
	if !strings.Contains(profile, `(deny file-write*)`) {
		t.Fatalf("profile に deny file-write* が含まれること: %s", profile)
	}
	if !strings.Contains(profile, `(allow file-write* (subpath "/private/tmp/work"))`) {
		t.Fatalf("/tmp -> /private/tmp の解決が反映されること: %s", profile)
	}
}

func TestBuildSeatbeltProfile_NilAllowPaths(t *testing.T) {
	profile := buildSeatbeltProfile(nil)
	if !strings.Contains(profile, `(deny file-write*)`) {
		t.Fatalf("deny file-write* を含むこと: %s", profile)
	}
	if !strings.Contains(profile, `(allow file-write* (subpath "/tmp"))`) {
		t.Fatalf("一時ディレクトリ行を含むこと: %s", profile)
	}
	if !strings.Contains(profile, `(allow file-write* (subpath "/private/tmp"))`) {
		t.Fatalf("一時ディレクトリ行を含むこと: %s", profile)
	}
	if strings.Contains(profile, `(allow file-write* (subpath ""))`) {
		t.Fatalf("allow_paths 由来の空行が無いこと: %s", profile)
	}
}

func TestBuildSeatbeltProfile_DeviceFileLines(t *testing.T) {
	profile := buildSeatbeltProfile(nil)
	for _, want := range []string{
		`(allow file-write* (literal "/dev/null"))`,
		`(allow file-write* (literal "/dev/tty"))`,
		`(allow file-write* (subpath "/dev/fd"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile に %q が含まれること: %s", want, profile)
		}
	}
	for _, notWant := range []string{
		`(literal "/dev/stdout")`,
		`(literal "/dev/stderr")`,
	} {
		if strings.Contains(profile, notWant) {
			t.Fatalf("profile に %q が含まれてはいけない (シンボリックリンクは literal で照合できない): %s", notWant, profile)
		}
	}
}

func TestCanonicalizeForProfile_Tmp(t *testing.T) {
	if got := canonicalizeForProfile("/tmp"); got != "/private/tmp" {
		t.Fatalf("got %q, want /private/tmp", got)
	}
}

func TestCanonicalizeForProfile_NonExistentPathWalksUpToExistingAncestor(t *testing.T) {
	got := canonicalizeForProfile("/tmp/does-not-exist-08-os-sandbox/x")
	want := "/private/tmp/does-not-exist-08-os-sandbox/x"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalizeForProfile_RelativePathResolvesAbsolute(t *testing.T) {
	got := canonicalizeForProfile("relative-does-not-exist")
	if !filepath.IsAbs(got) {
		t.Fatalf("相対パスは絶対パスへ解決されること: %q", got)
	}
}

func TestTemporaryWritablePaths_IncludesTMPDIR(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	resolved := canonicalizeForProfile(dir)
	paths := temporaryWritablePaths()
	found := false
	for _, p := range paths {
		if p == resolved {
			found = true
		}
	}
	if !found {
		t.Fatalf("TMPDIR の解決済みパスが含まれること: paths=%v want=%q", paths, resolved)
	}
}

func TestOSSandboxPlatformSupported_DarwinTrue(t *testing.T) {
	if !osSandboxPlatformSupported() {
		t.Fatal("darwin ビルドでは true")
	}
}

func TestOSSandboxAvailable_DarwinHasSandboxExec(t *testing.T) {
	if !osSandboxAvailable() {
		t.Fatal("darwin には sandbox-exec が標準搭載されているはず")
	}
}

func TestWrapWithOSSandbox_BuildsSandboxExecCommand(t *testing.T) {
	cmd, err := wrapWithOSSandbox(context.Background(), []string{"/tmp/work"}, "echo", []string{"hi"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.HasSuffix(cmd.Path, "sandbox-exec") {
		t.Fatalf("Path は sandbox-exec の解決パスであること: %q", cmd.Path)
	}
	if len(cmd.Args) < 4 || cmd.Args[1] != "-p" {
		t.Fatalf("Args に -p フラグが含まれること: %v", cmd.Args)
	}
	if !strings.Contains(cmd.Args[2], "/private/tmp/work") {
		t.Fatalf("Args[2] (profile) に allow_paths が含まれること: %s", cmd.Args[2])
	}
	if cmd.Args[3] != "echo" || cmd.Args[4] != "hi" {
		t.Fatalf("元コマンド名と引数が末尾に付くこと: %v", cmd.Args)
	}
}

// --- darwin 実挙動テスト (5.1節) ---

func TestDarwinRealBehavior_WriteOutsideAllowPathsIsDenied(t *testing.T) {
	allowed := t.TempDir()
	// t.TempDir() は $TMPDIR 配下であり、temporaryWritablePaths() が無条件に許可する
	// ため denied には使えない (allow_paths の効果を検証できない)。TMPDIR の外側で
	// あるカレントディレクトリ配下に作る
	denied, err := os.MkdirTemp(".", "os-sandbox-denied-")
	if err != nil {
		t.Fatalf("MkdirTemp err=%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(denied) })
	cmd, werr := wrapWithOSSandbox(context.Background(), []string{allowed}, "tee", []string{filepath.Join(denied, "should_not_exist")})
	if werr != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", werr)
	}
	cmd.Stdin = strings.NewReader("data")
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("allow_paths 外への書込みは拒否されるべき: out=%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(denied, "should_not_exist")); statErr == nil {
		t.Fatal("denied パスにファイルが作成されてはいけない")
	}
}

func TestDarwinRealBehavior_WriteInsideAllowPathsSucceeds(t *testing.T) {
	allowed := t.TempDir()
	target := filepath.Join(allowed, "ok.txt")
	cmd, err := wrapWithOSSandbox(context.Background(), []string{allowed}, "tee", []string{target})
	if err != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", err)
	}
	cmd.Stdin = strings.NewReader("data")
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("allow_paths 内への書込みは成功するべき: err=%v out=%s", runErr, out)
	}
	b, rerr := os.ReadFile(target)
	if rerr != nil || string(b) != "data" {
		t.Fatalf("書き込み内容 got=%q err=%v", string(b), rerr)
	}
}

func TestDarwinRealBehavior_MktempDirIsWritableViaAllowPaths(t *testing.T) {
	mkOut, mkErr := exec.Command("mktemp", "-d").CombinedOutput()
	if mkErr != nil {
		t.Fatalf("mktemp -d 失敗: %v (%s)", mkErr, mkOut)
	}
	dir := strings.TrimSpace(string(mkOut))
	if dir == "" {
		t.Fatal("mktemp -d が空を返した")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	target := filepath.Join(dir, "ok.txt")
	cmd, werr := wrapWithOSSandbox(context.Background(), []string{dir}, "tee", []string{target})
	if werr != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", werr)
	}
	cmd.Stdin = strings.NewReader("data")
	cout, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("mktemp -d 由来のパスへの書込みは成功するべき (EvalSymlinks 無しだと失敗する): err=%v out=%s", runErr, cout)
	}
}

func TestDarwinRealBehavior_DevNullWriteSucceeds(t *testing.T) {
	cmd, err := wrapWithOSSandbox(context.Background(), nil, "tee", []string{"/dev/null"})
	if err != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", err)
	}
	cmd.Stdin = strings.NewReader("data")
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("/dev/null への書込みは成功するべき: err=%v out=%s", runErr, out)
	}
}

func TestDarwinRealBehavior_DevStdoutWriteSucceeds(t *testing.T) {
	cmd, err := wrapWithOSSandbox(context.Background(), nil, "tee", []string{"/dev/stdout"})
	if err != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", err)
	}
	cmd.Stdin = strings.NewReader("data")
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("/dev/stdout への書込みは成功するべき (subpath /dev/fd 無いと Operation not permitted): err=%v out=%s", runErr, out)
	}
}

func TestDarwinRealBehavior_DevStderrWriteSucceeds(t *testing.T) {
	cmd, err := wrapWithOSSandbox(context.Background(), nil, "tee", []string{"/dev/stderr"})
	if err != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", err)
	}
	cmd.Stdin = strings.NewReader("data")
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("/dev/stderr への書込みは成功するべき: err=%v out=%s", runErr, out)
	}
}

// TestDarwinRealBehavior_DevTtyWriteSucceeds 制御端末の無いテスト環境では
// /dev/tty のオープン自体が失敗するため、擬似端末を割り当てた子プロセスから検証する。
// (literal "/dev/tty") が profile に無い場合、ここは Operation not permitted になる
func TestDarwinRealBehavior_DevTtyWriteSucceeds(t *testing.T) {
	cmd, err := wrapWithOSSandbox(context.Background(), nil, "tee", []string{"/dev/tty"})
	if err != nil {
		t.Fatalf("wrapWithOSSandbox err=%v", err)
	}
	f, startErr := pty.Start(cmd)
	if startErr != nil {
		t.Fatalf("pty.Start err=%v", startErr)
	}

	if _, werr := f.Write([]byte("data\n")); werr != nil {
		t.Fatalf("pty write err=%v", werr)
	}

	reader := bufio.NewReader(f)
	line, rerr := reader.ReadString('\n')
	// tee は stdin (pty slave) が EOF になるまで終了しない。master 側を閉じて
	// EOF を発生させてから Wait する。Wait を待たずに閉じるだけだと tee が
	// ゾンビ化しうるため、閉じたうえで有限時間の Wait を行う
	_ = f.Close()
	if rerr != nil {
		t.Fatalf("pty から tee の出力を読めること: err=%v line=%q", rerr, line)
	}
	if strings.TrimSpace(line) != "data" {
		t.Fatalf("tee /dev/tty の出力が echo されること: got=%q", line)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tee プロセスの終了待ちがタイムアウトしました")
	}
}
