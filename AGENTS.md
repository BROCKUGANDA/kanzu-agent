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
```
