package enricher

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestDetectLanguages(t *testing.T) {
	detectors := buildDetectors()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "Ruby code",
			text: "以下のRubyコードのバグを特定してください。\n```ruby\nclass Config\n  def initialize\n    @options = {}\n  end\nend\n```",
			want: []string{"ruby"},
		},
		{
			name: "Go code",
			text: "以下のGoコードのバグを全て特定してください。\n```go\nfunc main() {\n    var wg sync.WaitGroup\n    defer fmt.Println(\"done\")\n}\n```",
			want: []string{"go"},
		},
		{
			name: "no language",
			text: "今日の天気を教えてください",
			want: nil,
		},
		{
			name: "React code",
			text: "ReactのuseStateとuseEffectを使ったコンポーネントのバグを教えてください",
			want: []string{"react"},
		},
		{
			name: "Rust code",
			text: "以下のRustコードのコンパイルエラーの原因は？\n```rust\nfn main() {\n    let mut v = Vec::new();\n    let r = &v;\n    v.push(1);\n    println!(\"{:?}\", r);\n}\n```",
			want: []string{"rust"},
		},
		{
			name: "Java code",
			text: "以下のJavaコードの出力は？\n```java\npublic class Main {\n    public static void main(String[] args) {\n        System.out.println(\"hello\");\n    }\n}\n```",
			want: []string{"java"},
		},
		{
			name: "CSharp code",
			text: "以下のC#コードの挙動を教えてください。\n```csharp\nusing System;\nclass Program {\n    static void Main(string[] args) {\n        Console.WriteLine(\"hello\");\n    }\n}\n```",
			want: []string{"csharp"},
		},
		{
			name: "SpringBoot code",
			text: "Spring Bootで@RestControllerと@Serviceと@Autowiredを使ったDIの問題を教えてください",
			want: []string{"springboot"},
		},
		{
			name: "threshold not met",
			text: "defを使ってみたい",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLanguages(tt.text, detectors)
			if len(got) != len(tt.want) {
				t.Fatalf("detectLanguages() = %v, want %v", got, tt.want)
			}
			for _, w := range tt.want {
				if !slices.Contains(got, w) {
					t.Errorf("expected language %q not found in %v", w, got)
				}
			}
		})
	}
}

func TestNew_Disabled(t *testing.T) {
	fn := New(Config{Enabled: false}, ".")
	if fn != nil {
		t.Fatal("disabled config should return nil")
	}
}

func TestNew_InjectsContext(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "ruby.md"), []byte("Ruby spec reference"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Enabled:    true,
		PromptsDir: "prompts",
		Languages: map[string]string{
			"ruby": "ruby.md",
		},
	}
	fn := New(cfg, dir)
	if fn == nil {
		t.Fatal("enricher should not be nil")
	}

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "以下のRubyコードのバグを特定してください。\nclass Config\n  def initialize\n  end\nend"},
	}
	result, err := fn(context.Background(), msgs)
	if err != nil {
		t.Fatalf("enricher error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != llm.RoleSystem {
		t.Error("first message should be system")
	}
	if result[1].Role != llm.RoleUser {
		t.Error("second message should be injected context")
	}
	if result[2].Role != llm.RoleUser {
		t.Error("third message should be original user message")
	}
}

func TestNew_NoMatchSkips(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "ruby.md"), []byte("Ruby spec"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Enabled:    true,
		PromptsDir: "prompts",
		Languages:  map[string]string{"ruby": "ruby.md"},
	}
	fn := New(cfg, dir)
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "今日の天気は？"},
	}
	result, err := fn(context.Background(), msgs)
	if err != nil {
		t.Fatalf("enricher error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("no enrichment expected, got %d messages", len(result))
	}
}
