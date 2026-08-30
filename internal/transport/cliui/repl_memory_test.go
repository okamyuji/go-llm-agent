package cliui_test

import (
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

func newMemStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestREPL_MemoryWithoutStoreShowsDisabled(t *testing.T) {
	svc := &inputCapturingSvc{}
	got := runSlashREPL(t, svc, cliui.Options{}, "/memory\n# remember me\n/quit\n")
	if strings.Count(got, "メモリ機能は無効") != 2 {
		t.Fatalf("無効メッセージが 2 回出ていない: %q", got)
	}
	if len(svc.inputs) != 0 {
		t.Fatalf("LLM へ送られた: %d 回", len(svc.inputs))
	}
}

func TestREPL_MemoryListsFilesAndIndex(t *testing.T) {
	st := newMemStore(t)
	if err := st.Write("MEMORY.md", "- INDEX LINE\n", false); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("topic.md", "TOPIC BODY", false); err != nil {
		t.Fatal(err)
	}
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{MemoryStore: st}, "/memory\n/quit\n")
	for _, want := range []string{"MEMORY.md", "topic.md", "- INDEX LINE"} {
		if !strings.Contains(got, want) {
			t.Errorf("/memory 出力に %q が無い: %q", want, got)
		}
	}
	if strings.Contains(got, "TOPIC BODY") {
		t.Errorf("一覧表示でトピック本文まで出ている: %q", got)
	}
}

func TestREPL_MemoryShowsTopicFile(t *testing.T) {
	st := newMemStore(t)
	if err := st.Write("topic.md", "TOPIC BODY", false); err != nil {
		t.Fatal(err)
	}
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{MemoryStore: st}, "/memory topic.md\n/memory missing.md\n/quit\n")
	if !strings.Contains(got, "TOPIC BODY") {
		t.Fatalf("トピック本文が出ない: %q", got)
	}
	if !strings.Contains(got, "missing.md") || !strings.Contains(got, "読めません") {
		t.Fatalf("不存在ファイルのエラー表示が無い: %q", got)
	}
}

func TestREPL_HashPrefixAppendsMemory(t *testing.T) {
	st := newMemStore(t)
	svc := &inputCapturingSvc{}
	got := runSlashREPL(t, svc, cliui.Options{MemoryStore: st}, "# ユーザーは日本語を好む\n#\n/quit\n")
	if len(svc.inputs) != 0 {
		t.Fatalf("# 入力が LLM へ送られた: %d 回", len(svc.inputs))
	}
	if !strings.Contains(got, "memories.md") {
		t.Fatalf("保存先の表示が無い: %q", got)
	}
	body, err := st.Read("memories.md", 1<<20)
	if err != nil || !strings.Contains(body, "- ユーザーは日本語を好む") {
		t.Fatalf("memories.md 追記結果 %q err=%v", body, err)
	}
	index, err := st.Read("MEMORY.md", 1<<20)
	if err != nil || !strings.Contains(index, "ユーザーは日本語を好む") {
		t.Fatalf("MEMORY.md 追記結果 %q err=%v", index, err)
	}
	if !strings.Contains(got, "本文を指定") {
		t.Fatalf("空の # に対する案内が無い: %q", got)
	}
}

func TestREPL_MemoryDisplayStripsTerminalControls(t *testing.T) {
	st := newMemStore(t)
	// ESC[2J (画面消去) と OSC タイトル変更、C1 CSI (U+009B) を混ぜる
	payload := "safe\x1b[2Jline\ttab\n\x1b]0;evil\x07more\u009b31mend"
	if err := st.Write("topic.md", payload, false); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("MEMORY.md", "- \x1b[31mred\x1b[0m", false); err != nil {
		t.Fatal(err)
	}
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{MemoryStore: st}, "/memory topic.md\n/memory\n/quit\n")
	if strings.ContainsAny(got, "\x1b\x07\u009b") {
		t.Fatalf("制御文字が端末へ出力された: %q", got)
	}
	// 制御文字だけが落ち、残りは 1 文字も変わらないこと (置換や挿入があれば不一致になる)
	if !strings.Contains(got, "safe[2Jline\ttab\n]0;evilmore31mend") {
		t.Fatalf("サニタイズ結果が期待と異なる: %q", got)
	}
	if !strings.Contains(got, "- [31mred[0m") {
		t.Fatalf("索引のサニタイズ結果が期待と異なる: %q", got)
	}
}

func TestSanitizeTerminalBoundaries(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"改行とタブは残す", "a\nb\tc", "a\nb\tc"},
		{"0x1f は落とし 0x20 は残す", "a\x1fb c", "ab c"},
		{"DEL は落とし 0x7e は残す", "a\x7fb~", "ab~"},
		{"U+0080 と U+009F は落とし U+00A0 は残す", "a\u0080b\u009fc\u00a0d", "abc\u00a0d"},
		{"通常文字だけなら同じ", "hello 日本語", "hello 日本語"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliui.SanitizeTerminalForTest(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestREPL_HelpMentionsMemory(t *testing.T) {
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{}, "/help\n/quit\n")
	if !strings.Contains(got, "/memory") || !strings.Contains(got, "# <本文>") {
		t.Fatalf("help に /memory と # が無い: %q", got)
	}
}
