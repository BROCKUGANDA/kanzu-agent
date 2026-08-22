# ─────────────────────────────────────────────────────────────────────────────
# Kanzu Agent — multi-stage Dockerfile
#
# Stage 1 (builder): compiles the Go binary with the full vendor tree.
# Stage 2 (runtime): minimal Debian slim image with llama.cpp + model mount.
#
# The image ships everything except the 940 MB GGUF, which is mounted at runtime
# from a named volume.  This keeps the image under 200 MB while still proving
# the binary works in a clean, reproducible environment.
#
# Build:
#   docker build --platform linux/amd64 -t kanzu-agent:latest .
#   docker image inspect --format '{{index .RepoDigests 0}}' kanzu-agent:latest
#
# Run (interactive, model pre-downloaded on the host):
#   docker run --rm -it \
#     -v "$PWD/model:/app/model:ro" \
#     -v kanzu-data:/app/var \
#     kanzu-agent:latest ask "flag suspicious transactions this week"
#
# Run (doctor check, no model needed):
#   docker run --rm -it kanzu-agent:latest doctor
# ─────────────────────────────────────────────────────────────────────────────

# ── Stage 1: build ───────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS builder

WORKDIR /src

# Copy module files first so the layer is cached when only code changes.
COPY go.mod go.sum ./
COPY vendor/       ./vendor/

# Copy source.
COPY cmd/       ./cmd/
COPY internal/  ./internal/
COPY knowledge/ ./knowledge/
COPY fixtures/  ./fixtures/
COPY metadata.json ./

# Build a fully static Linux AMD64 binary.
# CGO_ENABLED=0 is the right choice here: modernc.org/sqlite is pure Go.
RUN CGO_ENABLED=0 GOARCH=amd64 GOOS=linux \
    go build -mod=vendor \
    -trimpath \
    -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" \
    -o /out/kanzu \
    ./cmd/kanzu

# ── Stage 2: runtime ─────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

# Install the llama.cpp CPU-only binary from the official GitHub release.
# Pin the exact build tag that was tested (b10580) so the image is reproducible.
ARG LLAMA_TAG=b10580
ARG LLAMA_URL=https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_TAG}/llama-${LLAMA_TAG}-bin-ubuntu-x64.zip

RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        unzip \
        libgomp1 && \
    rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "${LLAMA_URL}" -o /tmp/llama.zip && \
    unzip -q /tmp/llama.zip -d /tmp/llama && \
    install -m 0755 /tmp/llama/llama-cli    /usr/local/bin/llama-cli    2>/dev/null || true && \
    install -m 0755 /tmp/llama/llama-completion /usr/local/bin/llama-completion 2>/dev/null || true && \
    rm -rf /tmp/llama /tmp/llama.zip

# Non-root user for security.
RUN useradd -m -u 1000 kanzu
USER kanzu
WORKDIR /app

# Copy the compiled binary.
COPY --from=builder /out/kanzu /usr/local/bin/kanzu

# Copy the static data that ships with the binary (knowledge base + fixtures).
# The model GGUF is NOT copied — it is mounted at runtime.
COPY --chown=kanzu:kanzu knowledge/  ./knowledge/
COPY --chown=kanzu:kanzu fixtures/   ./fixtures/
COPY --chown=kanzu:kanzu metadata.json ./
COPY --chown=kanzu:kanzu var/prompts/ ./var/prompts/

# var/ holds kanzu.db (runtime state) — use a named volume so it persists.
VOLUME ["/app/var", "/app/model"]

# Expose nothing — this is a CLI tool, not a server.
# KANZU_ROOT tells the agent where metadata.json lives.
ENV KANZU_ROOT=/app

# Default to doctor so a bare `docker run` gives a health-check output.
ENTRYPOINT ["kanzu"]
CMD ["doctor"]
