package enricher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Config enricher の設定
type Config struct {
	Enabled    bool              `yaml:"enabled"`
	PromptsDir string            `yaml:"prompts_dir"`
	Languages  map[string]string `yaml:"languages"`
	Dynamic    DynamicConfig     `yaml:"dynamic"`
}

// New 設定に基づいてコンテキスト拡充関数を返す。
// configDir は config.yaml の親ディレクトリで、相対パス解決に使う
func New(cfg Config, configDir string) func(ctx context.Context, msgs []llm.Message) ([]llm.Message, error) {
	if !cfg.Enabled {
		return nil
	}
	promptsDir := cfg.PromptsDir
	if promptsDir != "" && !filepath.IsAbs(promptsDir) {
		promptsDir = filepath.Join(configDir, promptsDir)
	}

	cache := make(map[string]string)
	for lang, file := range cfg.Languages {
		p := file
		if !filepath.IsAbs(p) {
			p = filepath.Join(promptsDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("enricher: failed to load language spec", "lang", lang, "path", p, "err", err)
			continue
		}
		cache[strings.ToLower(lang)] = strings.TrimSpace(string(b))
	}

	var ret *retriever
	if cfg.Dynamic.Enabled && len(cfg.Dynamic.Sources) > 0 {
		ret = newRetriever(cfg.Dynamic)
	}

	if len(cache) == 0 && ret == nil {
		slog.Warn("enricher: no language specs loaded and dynamic retrieval disabled, enricher disabled")
		return nil
	}

	detectors := buildDetectors()

	return func(ctx context.Context, msgs []llm.Message) ([]llm.Message, error) {
		userText := extractLastUserMessage(msgs)
		if userText == "" {
			return msgs, nil
		}

		detected := detectLanguages(userText, detectors)
		if len(detected) == 0 {
			return msgs, nil
		}

		// 動的検索を優先し、結果が空なら静的specにフォールバックする
		var specText string
		mode := "dynamic"
		if ret != nil {
			specText = ret.retrieve(ctx, detected, userText)
		}
		if specText == "" {
			mode = "static"
			var specParts []string
			for _, lang := range detected {
				if spec, ok := cache[lang]; ok {
					specParts = append(specParts, spec)
				}
			}
			if len(specParts) == 0 {
				return msgs, nil
			}
			specText = strings.Join(specParts, "\n\n---\n\n")
		}

		contextMsg := llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("[CONTEXT: 言語仕様リファレンス]\n以下はコードの正確な評価のための言語仕様です。回答時にこの情報を参照してください。\n\n%s\n[END CONTEXT]", specText),
		}

		slog.Info("enricher: injected language specs", "languages", detected, "mode", mode)

		result := make([]llm.Message, 0, len(msgs)+1)
		sysIdx := -1
		for i, m := range msgs {
			if m.Role == llm.RoleSystem {
				sysIdx = i
				break
			}
		}
		if sysIdx >= 0 {
			result = append(result, msgs[:sysIdx+1]...)
			result = append(result, contextMsg)
			result = append(result, msgs[sysIdx+1:]...)
		} else {
			result = append(result, contextMsg)
			result = append(result, msgs...)
		}
		return result, nil
	}
}

func extractLastUserMessage(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

type langDetector struct {
	lang     string
	keywords []string
}

func buildDetectors() []langDetector {
	return []langDetector{
		{lang: "ruby", keywords: []string{
			"ruby", "Ruby", "def ", "end\n", "puts ", "class ", "module ",
			"Proc.new", "lambda {", "lambda{", ".each", ".map", ".select",
			"attr_", "require ", "gem ", "rails", "frozen_string",
			"eql?", "equal?", ".freeze", ".dup", ".nil?",
		}},
		{lang: "go", keywords: []string{
			"go ", "Go ", "Golang", "golang", "func ", "package ",
			"goroutine", "chan ", "defer ", "sync.", "fmt.",
			"interface{}", "interface {", ":= ", "var ", "import (",
			"go func", "wg.Add", "wg.Wait", "wg.Done",
		}},
		{lang: "python", keywords: []string{
			"python", "Python", "self.", "__init__", "print(",
			"pip ", "django", "flask", "pytest", "async def",
			"elif ", "except ", "raise ", "from __future__",
		}},
		{lang: "javascript", keywords: []string{
			"javascript", "JavaScript", "const ", "let ", "var ",
			"function ", "=> ", "async ", "await ", "Promise",
			"console.log", "require(", "import ", "export ",
			"node", "npm", "vue",
		}},
		{lang: "typescript", keywords: []string{
			"typescript", "TypeScript", "interface ", ": string",
			": number", ": boolean", "type ", "<T>", "as ",
			"enum ", "readonly ", "unknown", "never", "tsconfig",
		}},
		{lang: "react", keywords: []string{
			"react", "React", "useState", "useEffect", "useMemo",
			"useCallback", "useRef", "useContext", "JSX", "props",
			"className=", "Component", "<div", "render(",
		}},
		{lang: "csharp", keywords: []string{
			"C#", "csharp", "using System", "namespace ", "Console.WriteLine",
			"async Task", "IEnumerable", "get; set;", ".NET", "dotnet",
			"string[] args", "public sealed", "record ", "LINQ",
		}},
		{lang: "java", keywords: []string{
			"Java", "public class ", "public static void main", "System.out.println",
			"import java.", "@Override", "ArrayList<", "HashMap<",
			"extends ", "implements ", "JDK", "JVM", "Maven", "Gradle",
		}},
		{lang: "springboot", keywords: []string{
			"Spring Boot", "SpringBoot", "spring-boot", "@SpringBootApplication",
			"@RestController", "@Controller", "@Service", "@Repository",
			"@Autowired", "@GetMapping", "@PostMapping", "@Component",
			"@Entity", "application.properties", "application.yml", "JPA",
		}},
		{lang: "rust", keywords: []string{
			"Rust", "rust", "fn main", "let mut", "impl ", "trait ",
			"match ", "Option<", "Result<", "&str", "println!",
			"cargo", "Vec<", "Box<", "unwrap()", "borrow", "&mut",
		}},
	}
}

func detectLanguages(text string, detectors []langDetector) []string {
	scores := make(map[string]int)
	for _, d := range detectors {
		for _, kw := range d.keywords {
			if strings.Contains(text, kw) {
				scores[d.lang]++
			}
		}
	}
	if len(scores) == 0 {
		return nil
	}

	threshold := 2
	var result []string
	for lang, score := range scores {
		if score >= threshold {
			result = append(result, lang)
		}
	}
	return result
}
