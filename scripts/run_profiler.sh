#!/usr/bin/env bash
# Runs the ADTC profiler against this submission and emits submission.json.
# Usage:   bash scripts/run_profiler.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

# --- check model ---
if [ ! -f "model/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf" ]; then
    echo "Model not found. Run download_model.sh first."
    exit 1
fi

# --- set up venv ---
VENV="$HERE/.venv"
if [ ! -d "$VENV" ]; then
    python3 -m venv "$VENV"
fi
source "$VENV/bin/activate"
pip install --quiet --upgrade pip

# --- clone profiler if absent ---
PROFILER_DIR="$HERE/.tools/adtc-profiler"
if [ ! -d "$PROFILER_DIR" ]; then
    mkdir -p "$(dirname "$PROFILER_DIR")"
    echo "Cloning adtc-profiler into $PROFILER_DIR ..."
    git clone --depth 1 https://github.com/adtc-2026/adtc-profiler "$PROFILER_DIR"
fi

pip install --quiet -e "$PROFILER_DIR" 2>/dev/null \
    || pip install --quiet -r "$PROFILER_DIR/requirements.txt" 2>/dev/null \
    || true

# --- run ---
echo "Running ADTC profiler against metadata.json ..."
python -m adtc_profiler metadata.json --out submission.json 2>&1 \
    || python "$PROFILER_DIR/cli.py" metadata.json --out submission.json 2>&1 \
    || { echo "ERROR: profiler invocation failed. Check README at $PROFILER_DIR"; exit 1; }

echo "Output: $(pwd)/submission.json"
if [ -f submission.json ]; then
    echo "Size: $(wc -c < submission.json) bytes"
fi