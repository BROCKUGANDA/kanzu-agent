# Kanzu Agent — contributing notes

Offline-first, trilingual (EN/SW/LG) AML compliance copilot for Ugandan SACCOs.
Go 1.25 module; vendored dependencies; no runtime network access.

## Build, lint, test

```
go build -mod=vendor ./...
go vet -mod=vendor ./...
golangci-lint run ./...        # uses .golangci.yml
go test  -mod=vendor -race -count=1 -timeout 120s ./...
```

`-race` needs cgo and a C toolchain. On a Windows dev box without gcc, drop it
(`go test -mod=vendor -count=1 -timeout 120s ./...`); CI's Linux runner keeps it.

## Verify health

```
go run ./cmd/kanzu doctor
```

Every `ok` line must pass. The cross-language retrieval smoke test
(`en`, `sw`, `lg`) is the african_alpha claim gate — do not regress it.

## Offline claim

`cmd/` and `internal/` must not import `net/http`. CI enforces this.
Verify with: the `offline-check` job in `.github/workflows/ci.yml`.

## Submission files

- `metadata.json` is the single source of truth for the agent.
- `submission.json` is the profiler's copy; keep `submission.submission.*`
  in sync with `metadata.json` (CI checks this).
- `submission.json.reproducibility.docker_image_digest` is patched by
  the `docker.yml` workflow on push to `main` (GHCR) — do not edit by hand.

## Docker

```
docker build --platform linux/amd64 -t kanzu-agent:latest .
docker run --rm --entrypoint sh kanzu-agent:latest -c "kanzu init >/dev/null; kanzu doctor"
```

The image ships `llama-cli` but not the 940 MB GGUF; mount it with
`-v "$PWD/model:/app/model:ro"`. `kanzu init` seeds the ledger inside the
container, so a bare `doctor` reports empty tables until you run it.

Notes for this dev box:
- Docker Desktop is a **per-user** install at
  `%LOCALAPPDATA%\Programs\DockerDesktop\resources\bin`, which is not on PATH.
  Prepend it before building, otherwise buildx fails with
  `docker-credential-desktop: executable file not found`.
- The llama.cpp Linux CPU release asset is a `.tar.gz` (`.zip` assets are
  Windows-only) and is dynamically linked, so the `*.so` files must be
  installed and `ldconfig` run. The Dockerfile ends that stage with
  `llama-cli --version` so a bad URL fails the build instead of silently
  shipping an image with no inference binary.
- `.dockerignore` excludes `var/` but re-includes `var/prompts/` because the
  Dockerfile copies the six versioned system prompts.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **kanzu-agent** (1250 symbols, 3965 relationships, 108 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> Index stale? Run `node .gitnexus/run.cjs analyze` from the project root — it auto-selects an available runner. No `.gitnexus/run.cjs` yet? `npx gitnexus analyze` (npm 11 crash → `npm i -g gitnexus`; #1939).

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows. For regression review, compare against the default branch: `detect_changes({scope: "compare", base_ref: "main"})`.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `query({search_query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `context({name: "symbolName"})`.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method without first running `impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `rename` which understands the call graph.
- NEVER commit changes without running `detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/kanzu-agent/context` | Codebase overview, check index freshness |
| `gitnexus://repo/kanzu-agent/clusters` | All functional areas |
| `gitnexus://repo/kanzu-agent/processes` | All execution flows |
| `gitnexus://repo/kanzu-agent/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
