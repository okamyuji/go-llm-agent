# Repository instructions

## Command execution

- Prefix shell commands with `rtk`.
- Run commands from the repository root unless a command explicitly says otherwise.
- Treat `.github/workflows/ci.yml` and `scripts/quality-gate.sh` as the source of truth for CI behavior. Do not invent a lighter local substitute.

## Engineering rules

- Before implementation, state acceptance criteria that can be verified by a command, test, or observable result.
- Keep each change focused on one reason. Do not include unrelated cleanup or refactoring.
- Extend an existing package, interface, script, or convention before introducing a new abstraction. Apply YAGNI.
- Record durable design decisions in `_docs/`; chat-only decisions are not sufficient.
- Never commit credentials, tokens, `.env`, `config.yaml`, certificates, private keys, or machine-specific absolute paths.
- MCP integrations, destructive commands, and externally visible mutations are opt-in. Explain the exact effect and obtain explicit approval before running them.
- Preserve unrelated working-tree changes. Do not overwrite or reformat files outside the requested scope.

## Definition of Done

Every completion report must include:

1. The acceptance criteria and whether each criterion passed.
2. The exact verification commands run and their outcomes.
3. Any checks not run, with the reason.
4. Any difference between local commands and CI. If there is no difference, say so explicitly.

Before claiming Done, run the documented checks relevant to the change:

```bash
rtk bash scripts/quality-gate.sh
```

Expected outcome: exit code 0 and `all quality checks passed`. This is the same quality-gate script used by CI and covers formatting, vet, static analysis, vulnerability scanning, race-enabled tests, build, and secret scanning.

For changes affecting CLI flows, HTTP behavior, fixtures, integration boundaries, or `tests/e2e/`, also run:

```bash
rtk env RUN_E2E=1 bash scripts/quality-gate.sh
```

Expected outcome: exit code 0, all E2E scripts pass, and `all quality checks passed`. CI runs this variant in its `e2e` job.

For security hardening or sandbox changes, also run:

```bash
rtk bash scripts/verify-hardening.sh
```

Expected outcome: exit code 0. CI runs this separately in its `pre-commit` job.

If local tooling or environment prevents an exact CI command, do not substitute silently. Report the missing prerequisite, the command actually run, and the resulting coverage gap.

## Change documentation

- Put architectural choices, security tradeoffs, compatibility decisions, and intentional CI/local differences in `_docs/`.
- Do not create a design document for a mechanical change with no durable decision.
- Keep user-facing operation instructions in `README.md`; keep implementation rationale in `_docs/`.

See `_docs/engineering-practices.md` for the rationale and CI mapping behind these rules.
