# Benchmark working-tree audit — 2026-06-21

## Decision

The uncommitted changes to `bench-config.yaml` and `bench-config-plain.yaml` are rejected. The untracked `config.rb` is also rejected.

The benchmark configurations remain at their committed values. This decision is based on the measurements below rather than an assumption that the previous configuration is preferable.

## Environment

- Agent: current working-tree build
- Runtime: local Ollama
- Available model: `hf.co/sakamakismile/gemma-4-12B-coder-fable5-composer2.5-GGUF:Q5_K_M`
- Suite: the six questions in `scripts/bench_enricher.sh`
- Decoding: `temperature: 0`

The previously configured `gemma4:e4b` model was not installed, so no claim is made that either model is better. A model replacement without a direct comparison is not accepted as an improvement.

## Results

| Configuration | Exact acceptable answers | Total time |
|---|---:|---:|
| Plain, `think` omitted | 3/6 | 208 s |
| Plain, `think: false` | 3/6 | 157 s |
| Enricher enabled, `think` omitted | 4/6 | 277 s |

An answer was accepted only when its final result and explanation were internally consistent and any requested correction was usable. Contradictory answers and truncated code were failures.

The `think: false` removal produced no score improvement and increased the measured plain-suite time from 157 seconds to 208 seconds, approximately 33%. The removal is therefore rejected.

The enricher improved the net score by one answer for this model, but increased total time by approximately 33% and regressed one Ruby answer while improving two others. This confirms model- and question-dependent behavior; it does not justify the unrelated default-model edit.

## Operational findings

`scripts/bench_enricher.sh` passes `-model` for every request. Consequently, changing `default_model` in either benchmark configuration does not affect the script's normal benchmark execution. Since the change had no effect in that path and no direct old/new model comparison was possible, it is rejected.

`config.rb` failed `ruby -c config.rb` with unclosed conditional and `begin` blocks. It was an incomplete generated artifact, not a valid project source file, and was removed.
