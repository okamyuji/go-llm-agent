# Engineering practices decision

## Status

Accepted.

## Decision

Repository work uses verifiable acceptance criteria, focused changes, the existing architecture by default, and explicit evidence before completion. Durable design decisions live in `_docs/` so they survive beyond a chat session.

Potentially destructive operations and MCP integrations require explicit opt-in and approval. Secrets, credentials, local configuration, private key material, and machine-specific absolute paths must not be committed.

## Verification contract

`scripts/quality-gate.sh` is the shared local/CI quality entry point. It runs:

- gofmt verification
- mutation-diff package filtering regression test
- `go vet`
- `staticcheck`
- `golangci-lint`
- `govulncheck`
- race-enabled, uncached, shuffled Go tests with coverage
- a release-build smoke test
- staged sensitive-file protection
- gitleaks against the working tree

The normal local command is:

```bash
rtk bash scripts/quality-gate.sh
```

CI invokes the same script without the local `rtk` output-filtering wrapper. The executed quality-gate logic is otherwise identical.

The CI `e2e` job sets `RUN_E2E=1`, so locally equivalent coverage is:

```bash
rtk env RUN_E2E=1 bash scripts/quality-gate.sh
```

The CI `pre-commit` job additionally invokes `scripts/verify-hardening.sh`. Changes to security boundaries must run that script locally as well.

## Consequences

- A Done claim is auditable: it names commands, outcomes, omissions, and CI differences.
- Fast targeted tests may be used during development, but they do not replace the relevant documented gate before completion.
- New abstractions need a concrete current requirement; speculative extensibility is not sufficient.
- Unrelated cleanup is deferred to a separate change with its own reason and verification.
