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
