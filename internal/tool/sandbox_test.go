package tool_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestSandbox_DenyDotGit(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	target := filepath.Join(root, ".git", "HEAD")
	err := sb.CheckPath(target)
	if err == nil {
		t.Fatal(".git 配下は deny されるべき")
	}
	if !strings.Contains(err.Error(), "センシティブ") {
		t.Fatalf("センシティブ理由を含むこと: %v", err)
	}
}

func TestSandbox_DenyEnv(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	cases := []string{
		filepath.Join(root, ".env"),
		filepath.Join(root, "sub", ".env.local"),
		filepath.Join(root, "sub", ".env.production"),
		filepath.Join(root, "deep", "more", ".ssh", "config"),
	}
	for _, c := range cases {
		if err := sb.CheckPath(c); err == nil {
			t.Fatalf("env/ssh 系は deny されるべき: %s", c)
		}
	}
}

func TestSandbox_DenyIDKeys(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	for _, name := range []string{
		"id_rsa",
		"id_rsa.pub",
		"id_rsa_backup",
		"id_ed25519",
		"id_ed25519.pub",
		"id_ecdsa_old",
		"id_dsa.bak",
	} {
		p := filepath.Join(root, name)
		if err := sb.CheckPath(p); err == nil {
			t.Fatalf("%s は deny", name)
		}
	}
}

func TestSandbox_SensitivePatternsImmutable(t *testing.T) {
	got := tool.SensitivePatterns()
	if len(got) == 0 {
		t.Fatal("SensitivePatterns() は空でない一覧を返すべき")
	}
	// 返却されたスライスを変更しても内部状態は崩れない（コピーが返るため）
	got[0] = "MODIFIED"
	again := tool.SensitivePatterns()
	if again[0] == "MODIFIED" {
		t.Fatal("SensitivePatterns() は内部状態への書込を許してはならない")
	}
}

func TestSandbox_CustomDeny(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandboxWithDeny([]string{root}, []string{"secrets"})
	if err := sb.CheckPath(filepath.Join(root, "secrets", "k.txt")); err == nil {
		t.Fatal("追加 deny も効くべき")
	}
	if err := sb.CheckPath(filepath.Join(root, "ok.txt")); err != nil {
		t.Fatalf("通常パスは許可: %v", err)
	}
}

func TestSandbox_SymlinkEscape(t *testing.T) {
	// /tmp 配下にルートと外部ターゲットを作り、ルート内の symlink で外部を指す
	root := t.TempDir()
	outside := t.TempDir()
	outFile := filepath.Join(outside, "leak.txt")
	if err := os.WriteFile(outFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "link.txt")
	if err := os.Symlink(outFile, linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	sb := tool.NewSandbox([]string{root})
	if err := sb.CheckPath(linkPath); err == nil {
		t.Fatal("symlink で root 外部を指す場合は deny されるべき")
	}
}

func TestSandbox_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	rel := filepath.Join(root, "..", "outside.txt")
	if err := sb.CheckPath(rel); err == nil {
		t.Fatal(".. による上位ディレクトリ参照は deny")
	}
}

func TestSandbox_AllowsConfiguredNonSensitivePath(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	if err := sb.CheckPath(filepath.Join(root, "src", "foo.go")); err != nil {
		t.Fatalf("通常パスは許可: %v", err)
	}
}

func TestSandbox_AllowsNonExistentChildForWrite(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	if err := sb.CheckPath(filepath.Join(root, "new_dir", "new_file.txt")); err != nil {
		t.Fatalf("未存在の書込先も許可: %v", err)
	}
}
