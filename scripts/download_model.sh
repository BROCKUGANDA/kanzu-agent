#!/usr/bin/env bash
# download_model.sh — idempotent fetcher for the Kanzu Agent GGUF checkpoint.
#
# Contract (adtc-2026-submission-template):
#   - Public URL, no credentials.
#   - Idempotent: safe to re-run; skips download when the file already matches.
#   - Output path must match _runtime.model_path in metadata.json.
#
# Model provenance:
#   Base model:  Qwen/Qwen2.5-1.5B-Instruct
#   Quantized by: bartowski (GGUF Q4_K_M, 338 tensors, 1,543,714,304 params)
#   Source repo: https://huggingface.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF
#
# The Q4_K_M variant was chosen over Qwen's own GGUF build because Qwen's file
# materialises output.weight as a separate tensor, pushing the tensor-table sum
# to 1,777,088,000 — outside the ±15% fraud-check window against the 1.5B claim.
# See REPORT.md §3.2 for the full head-to-head.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODEL_DIR="$HERE/model"
MODEL_PATH="$MODEL_DIR/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"

EXPECTED_SHA256="1adf0b11065d8ad2e8123ea110d1ec956dab4ab038eab665614adba04b6c3370"
EXPECTED_BYTES=986048768

# --- Hugging Face resolve URL for the Q4_K_M variant ---
# bartowski repos ship GGUF files at the repo root.
# Try the canonical resolve URL first; if it fails (403), fall back to
# the hf.co subdomain which uses a different auth path.
MODEL_URL="https://huggingface.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"
FALLBACK_URL="https://hf.co/bartowski/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf"

# --- checked functions ---
check_model() {
    if [ ! -f "$MODEL_PATH" ]; then
        return 1
    fi
    actual_sha=$(sha256sum "$MODEL_PATH" 2>/dev/null | awk '{print $1}')
    if [ "$actual_sha" != "$EXPECTED_SHA256" ]; then
        echo "WARN: existing model sha256 mismatch ($actual_sha != $EXPECTED_SHA256). Re-downloading."
        return 1
    fi
    actual_bytes=$(stat -c%s "$MODEL_PATH" 2>/dev/null || stat -f%z "$MODEL_PATH" 2>/dev/null || echo 0)
    if [ "$actual_bytes" != "$EXPECTED_BYTES" ]; then
        echo "WARN: existing model byte count mismatch ($actual_bytes != $EXPECTED_BYTES). Re-downloading."
        return 1
    fi
    return 0
}

download() {
    echo "Downloading Qwen2.5-1.5B-Instruct-Q4_K_M.gguf from Hugging Face ..."
    echo "  URL:  $MODEL_URL"
    echo "  Dest: $MODEL_PATH"
    echo "  Expected sha256: $EXPECTED_SHA256"
    echo "  Expected bytes:  $EXPECTED_BYTES"
    echo ""

    mkdir -p "$MODEL_DIR"

    # Try primary URL first; fall back to hf.co subdomain if 403.
    downloaded=false
    if command -v curl >/dev/null 2>&1; then
        if curl -L --resume-from "$MODEL_PATH" -o "$MODEL_PATH" "$MODEL_URL" 2>/dev/null; then
            downloaded=true
        else
            echo "Primary URL failed (HTTP error); trying fallback..."
            curl -L --resume-from "$MODEL_PATH" -o "$MODEL_PATH" "$FALLBACK_URL" 2>/dev/null && downloaded=true
        fi
    elif command -v wget >/dev/null 2>&1; then
        if wget -c -O "$MODEL_PATH" "$MODEL_URL" 2>/dev/null; then
            downloaded=true
        else
            echo "Primary URL failed (HTTP error); trying fallback..."
            wget -c -O "$MODEL_PATH" "$FALLBACK_URL" 2>/dev/null && downloaded=true
        fi
    else
        echo "ERROR: neither curl nor wget found. Install one to download the model."
        return 1
    fi
    if [ "$downloaded" != "true" ]; then
        echo "ERROR: both URLs failed. Check network and HF repo visibility."
        return 1
    fi
}

verify() {
    echo ""
    echo "Verifying download ..."

    if [ ! -f "$MODEL_PATH" ]; then
        echo "ERROR: model file not found at $MODEL_PATH after download."
        return 1
    fi

    actual_sha=$(sha256sum "$MODEL_PATH" 2>/dev/null | awk '{print $1}')
    if [ -z "$actual_sha" ]; then
        # macOS/BSD stat fallback
        actual_sha=$(shasum -a 256 "$MODEL_PATH" 2>/dev/null | awk '{print $1}')
    fi

    if [ "$actual_sha" != "$EXPECTED_SHA256" ]; then
        echo "ERROR: sha256 mismatch."
        echo "  Expected: $EXPECTED_SHA256"
        echo "  Actual:   $actual_sha"
        echo "  The model file may be corrupt or the source URL changed."
        return 1
    fi

    actual_bytes=$(stat -c%s "$MODEL_PATH" 2>/dev/null || stat -f%z "$MODEL_PATH" 2>/dev/null || echo 0)
    echo "  sha256:  $actual_sha  (match)"
    echo "  bytes:   $actual_bytes  (expected $EXPECTED_BYTES)"
    if [ "$actual_bytes" != "$EXPECTED_BYTES" ]; then
        echo "WARN: byte count differs from expected — sha256 still matches, so this is likely fine."
    fi

    echo ""
    echo "Model ready: $MODEL_PATH"
    echo "Next: go run ./cmd/kanzu doctor"
}

# --- main ---
if check_model; then
    echo "Model already present and verified at $MODEL_PATH"
    echo "  sha256: $EXPECTED_SHA256"
    echo "  bytes:  $EXPECTED_BYTES"
    echo ""
    echo "Set KANZU_MODEL_PATH or re-run to force a fresh download."
    exit 0
fi

download || exit 1
verify || exit 1
