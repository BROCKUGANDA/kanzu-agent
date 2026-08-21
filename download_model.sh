#!/usr/bin/env bash
# Kanzu Agent — model acquisition for ADTC 2026 (Laptop LLM Challenge).
#
# Contract enforced by the ADTC evaluator:
#   - idempotent: safe to run repeatedly, skips work when the artifact is valid
#   - credential-free: public URL only, no HF token, no login, no git-lfs
#   - output path must equal `_runtime.model_path` in metadata.json
#
# The model is the unmodified upstream Q4_K_M quantisation of
# Qwen2.5-1.5B-Instruct (dense, 1.54B params). Nothing here runs at inference
# time: Hugging Face is used strictly as a public file host for this download.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODEL_DIR="$HERE/model"
MODEL_NAME="Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"
MODEL_FILE="$MODEL_DIR/$MODEL_NAME"

# Must match metadata.json `_runtime.model_sha256` / `_runtime.model_bytes`.
EXPECTED_SHA256="1adf0b11065d8ad2e8123ea110d1ec956dab4ab038eab665614adba04b6c3370"
EXPECTED_BYTES="986048768"

# Mirrors serve byte-identical content, so one checksum validates all of them.
# See REPORT.md for why this build was selected over Qwen's own GGUF (it ties
# output.weight, so its parameter count matches the model card and the profiler's
# fraud check; it is also 155 MB lighter in peak RSS at equal throughput).
# The hf-mirror entry exists because HF's CDN is intermittently unreachable from
# several African ISPs; it is a download-time convenience only and is never
# contacted at inference time.
MODEL_URLS=(
  "https://huggingface.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/${MODEL_NAME}?download=true"
  "https://hf-mirror.com/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/${MODEL_NAME}?download=true"
)

# Optional local seed: if the operator already has the exact artifact (verified
# by checksum below), point KANZU_MODEL_CACHE at it and skip the network
# entirely. Useful for airgapped installs and for re-running the pipeline on a
# metered connection.
LOCAL_CACHE="${KANZU_MODEL_CACHE:-}"

log()  { printf '[download_model] %s\n' "$*"; }
fail() { printf '[download_model] error: %s\n' "$*" >&2; exit 1; }

# ── checksum helper: coreutils on Linux, shasum on macOS ──────────────────────
sha256_of() {
  local target="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$target" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$target" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$target" | awk '{print $NF}'
  else
    printf 'UNVERIFIABLE'
  fi
}

size_of() {
  # stat is not portable between GNU and BSD; try both spellings.
  stat -c%s "$1" 2>/dev/null || stat -f%z "$1" 2>/dev/null || printf '0'
}

verify() {
  local target="$1"
  [[ -f "$target" ]] || return 1

  local actual_bytes
  actual_bytes="$(size_of "$target")"
  if [[ "$actual_bytes" != "$EXPECTED_BYTES" ]]; then
    log "size mismatch: expected ${EXPECTED_BYTES} bytes, found ${actual_bytes}"
    return 1
  fi

  local actual_sha
  actual_sha="$(sha256_of "$target")"
  if [[ "$actual_sha" == "UNVERIFIABLE" ]]; then
    # Size already matched exactly; refuse to claim a checksum we cannot compute.
    log "warning: no sha256 tool found (sha256sum/shasum/openssl) — verified size only"
    return 0
  fi
  if [[ "$actual_sha" != "$EXPECTED_SHA256" ]]; then
    log "sha256 mismatch:"
    log "  expected $EXPECTED_SHA256"
    log "  actual   $actual_sha"
    return 1
  fi

  # GGUF magic — guards against an HTML error page saved with the right length.
  local magic
  magic="$(head -c 4 "$target" | tr -d '\0')"
  [[ "$magic" == "GGUF" ]] || { log "file does not start with GGUF magic"; return 1; }

  return 0
}

fetch() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    # -C - resumes a partial .partial file; --retry survives flaky links.
    curl -L --fail --progress-bar --retry 5 --retry-delay 3 --retry-connrefused \
         -C - -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget --continue --tries=5 --waitretry=3 --show-progress -O "$dest" "$url"
  else
    fail "neither curl nor wget is available"
  fi
}

# ── main ─────────────────────────────────────────────────────────────────────
mkdir -p "$MODEL_DIR"

if verify "$MODEL_FILE"; then
  log "model already present and verified — skipping download"
  log "path: $MODEL_FILE"
  exit 0
fi

if [[ -f "$MODEL_FILE" ]]; then
  log "existing file failed verification — removing and re-fetching"
  rm -f "$MODEL_FILE"
fi

# Try the local cache before the network.
if [[ -n "$LOCAL_CACHE" && -f "$LOCAL_CACHE" ]]; then
  log "trying local cache: $LOCAL_CACHE"
  cp -f "$LOCAL_CACHE" "$MODEL_FILE.partial"
  mv -f "$MODEL_FILE.partial" "$MODEL_FILE"
  if verify "$MODEL_FILE"; then
    log "verified from local cache — no network used"
    log "done: $MODEL_FILE"
    exit 0
  fi
  log "local cache did not match expected checksum — falling back to download"
  rm -f "$MODEL_FILE"
fi

log "target: $MODEL_FILE (~940 MiB)"

for url in "${MODEL_URLS[@]}"; do
  log "fetching ${url%%\?*}"
  if fetch "$url" "$MODEL_FILE.partial"; then
    mv -f "$MODEL_FILE.partial" "$MODEL_FILE"
    if verify "$MODEL_FILE"; then
      log "verified sha256 $EXPECTED_SHA256"
      log "done: $MODEL_FILE"
      exit 0
    fi
    log "downloaded artifact failed verification — discarding and trying next mirror"
    rm -f "$MODEL_FILE"
  else
    log "mirror failed — trying next"
    # Keep .partial so the next attempt can resume from the same offset.
  fi
done

rm -f "$MODEL_FILE.partial"
fail "all mirrors exhausted; could not obtain a verified $MODEL_NAME"
