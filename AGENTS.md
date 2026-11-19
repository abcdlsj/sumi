# Sumi Engineering Guidelines

Sumi is a collaboration platform for humans and persistent AI agents.

## Core Rules

- Read `docs/DESIGN.md` before making changes. It is the single source of truth; fix code to match the spec, never add compatibility layers.
- Update `docs/DESIGN.md` first whenever product behavior, schema, or protocol changes.
- Keep architecture directional: adapters (`server`, `computer`, `cli`, `web`) -> `app` -> `domain` / `store`.
- Delete obsolete code and abstractions instead of keeping fallbacks.
- Keep comments brief and in English; only explain invariants, concurrency, or security constraints.
- Never log or print message bodies, attachments, memory contents, prompts, or secrets in logs, errors, or tests.

## Verification

- Write meaningful tests for domain invariants, concurrency, and security boundaries.
- Before committing Go changes: `gofmt -l .` (empty), `go vet ./...`, `go test ./...`.
- Before committing Web changes: `pnpm --dir web typecheck` and `pnpm --dir web test`.
- Commit each task separately with concise English commit messages.
