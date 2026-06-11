#!/bin/bash
set -euo pipefail

AGENT="/tmp/agent-bench"
CONFIG="/Users/yujiokamoto/devs/golang/go-llm-agent/bench-config.yaml"
OUTDIR="/tmp/bench_dynamic_v3"
mkdir -p "$OUTDIR"

MODELS=("ollama/gemma4:e4b" "ollama/qwen3.5:9b" "ollama/qwen2.5-coder:14b")

declare -A QUESTIONS

QUESTIONS[ruby_q1]='以下のRubyコードのバグを特定して修正コードを示してください。

```ruby
class Config
  DEFAULT_OPTIONS = { verbose: false, retries: 3, tags: [] }
  def initialize(overrides = {})
    @options = DEFAULT_OPTIONS.merge(overrides)
  end
  def add_tag(tag)
    @options[:tags] << tag
  end
end

c1 = Config.new
c1.add_tag('\''production'\'')
c2 = Config.new
puts c2.options[:tags].inspect
```'

QUESTIONS[ruby_q2]='以下のRubyコードの各putsの出力結果を正確に答えて理由を説明してください。

```ruby
a = '\''hello'\''
b = '\''hello'\''
puts a == b
puts a.eql?(b)
puts a.equal?(b)
```'

QUESTIONS[ruby_q3]='以下のRubyコードの出力結果を正確に答えてください。

```ruby
def test_proc
  p = Proc.new { |x| return x if x == 2 }
  [1,2,3].each(&p)
  '\''reached end'\''
end

def test_lambda
  l = lambda { |x| return x if x == 2 }
  [1,2,3].each(&l)
  '\''reached end'\''
end

puts test_proc
puts test_lambda
```'

QUESTIONS[ruby_q4]='以下のRubyコードの出力結果を正確に答えてください。

```ruby
GREETING = '\''hello'\''
GREETING << '\'' world'\''
puts GREETING
```'

QUESTIONS[go_q1]='以下のGoコードのバグを全て特定して修正してください。

```go
func main() {
    var wg sync.WaitGroup
    results := make([]int, 0)

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            results = append(results, i*2)
        }()
    }
    wg.Wait()
    fmt.Println(results)
}
```'

QUESTIONS[go_q2]='以下のGoコードの全ての出力を正確に実行順序通りに答えてください。

```go
func main() {
    var s1 []int
    s2 := []int{}
    fmt.Println(s1 == nil, s2 == nil)

    for i := 0; i < 3; i++ {
        defer fmt.Println("defer:", i)
    }

    var p *int = nil
    var iface interface{} = p
    fmt.Println(iface == nil)
    fmt.Println(p == nil)
}
```'

echo "=== Real Agent Enricher Benchmark (generalized spec files) ==="
echo ""

for model in "${MODELS[@]}"; do
    safe_model=$(echo "$model" | tr ':/' '_')
    echo "--- Model: $model ---"

    for qname in ruby_q1 ruby_q2 ruby_q3 ruby_q4 go_q1 go_q2; do
        prompt="${QUESTIONS[$qname]}"
        outfile="$OUTDIR/${safe_model}_${qname}"

        echo -n "  $qname ... "
        start_time=$(date +%s)

        response=$($AGENT run -config "$CONFIG" -p "$prompt" -model "$model" 2>"${outfile}.log") || true

        end_time=$(date +%s)
        elapsed=$((end_time - start_time))

        echo "$response" > "$outfile"
        echo "$elapsed" > "${outfile}.time"

        detected=$(grep -o 'languages=\[.*\]' "${outfile}.log" 2>/dev/null || echo "none")
        echo "done (${elapsed}s, $(wc -c < "$outfile" | tr -d ' ')B, detect: $detected)"
    done
    echo ""
done

echo "=== All done ==="
