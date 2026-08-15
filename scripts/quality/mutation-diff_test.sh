#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin"

cat > "$WORK/bin/git" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" != "diff" ]; then
  exec /usr/bin/git "$@"
fi
cat <<'DIFF'
diff --git a/internal/config/test_target.go b/internal/config/test_target.go
--- a/internal/config/test_target.go
+++ b/internal/config/test_target.go
@@ -0,0 +1 @@
+package config
DIFF
EOF

cat > "$WORK/bin/gremlins" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$2" >> "$GREMLINS_TEST_LOG"
if [ "${GREMLINS_TEST_LIVED:-0}" = "1" ]; then
  echo "  LIVED CONDITIONALS_NEGATION at test_target.go:1:1"
fi
EOF

chmod +x "$WORK/bin/git" "$WORK/bin/gremlins"

if GREMLINS_TEST_LOG="$WORK/no-package-gremlins.log" \
  PATH="$WORK/bin:$PATH" \
  "$ROOT/scripts/quality/mutation-diff.sh" HEAD \
  > "$WORK/no-package.out" 2>&1; then
  echo "mutation-diff unexpectedly succeeded without a Go package" >&2
  exit 1
fi
if ! grep -q "at least one Go package is required" "$WORK/no-package.out"; then
  cat "$WORK/no-package.out" >&2
  echo "missing error for empty package list" >&2
  exit 1
fi

GREMLINS_TEST_LOG="$WORK/gremlins.log" \
  PATH="$WORK/bin:$PATH" \
  "$ROOT/scripts/quality/mutation-diff.sh" HEAD \
    ./tests/e2e/fixtures/repl_basic_exercise \
    github.com/okamyuji/go-llm-agent/tests/e2e/fixtures/repl_basic_exercise \
    ./internal/config \
    github.com/okamyuji/go-llm-agent/internal/config

actual="$(cat "$WORK/gremlins.log")"
expected="./internal/config
github.com/okamyuji/go-llm-agent/internal/config"
if [ "$actual" != "$expected" ]; then
  printf 'gremlins packages:\n%s\nwant:\n%s\n' "$actual" "$expected" >&2
  exit 1
fi

if GREMLINS_TEST_LOG="$WORK/invalid-gremlins.log" \
  PATH="$WORK/bin:$PATH" \
  "$ROOT/scripts/quality/mutation-diff.sh" HEAD ./tests \
  > "$WORK/invalid-target.out" 2>&1; then
  echo "non-package ancestor path unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -q "not exactly one Go package: ./tests" "$WORK/invalid-target.out"; then
  cat "$WORK/invalid-target.out" >&2
  echo "missing error for non-package ancestor path" >&2
  exit 1
fi

if GREMLINS_TEST_LOG="$WORK/multiple-gremlins.log" \
  PATH="$WORK/bin:$PATH" \
  "$ROOT/scripts/quality/mutation-diff.sh" HEAD './tests/...' \
  > "$WORK/multiple-target.out" 2>&1; then
  echo "multiple-package pattern unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -q "not exactly one Go package: ./tests/..." "$WORK/multiple-target.out"; then
  cat "$WORK/multiple-target.out" >&2
  echo "missing error for multiple-package pattern" >&2
  exit 1
fi

if GREMLINS_TEST_LOG="$WORK/lived-gremlins.log" \
  GREMLINS_TEST_LIVED=1 \
  PATH="$WORK/bin:$PATH" \
  "$ROOT/scripts/quality/mutation-diff.sh" HEAD \
    github.com/okamyuji/go-llm-agent/internal/config \
  > "$WORK/lived.out" 2>&1; then
  cat "$WORK/lived.out" >&2
  echo "import-path target ignored a LIVED mutant on a changed line" >&2
  exit 1
fi
if ! grep -q "SURVIVED ON CHANGED LINE" "$WORK/lived.out"; then
  cat "$WORK/lived.out" >&2
  echo "missing changed-line survivor for import-path target" >&2
  exit 1
fi

echo "OK: tests/e2e packages are skipped before gremlins starts"
