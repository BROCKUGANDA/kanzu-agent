#!/usr/bin/env bash
# Runs the ADTC profiler against this submission and emits submission.json.
#
# The upstream CLI shape is:
#   adtc-profiler run --submission <dir> --mode participant --output <file>
#                     [--skip-accuracy] [--seed 42]
#
# Usage:
#   bash scripts/run_profiler.sh                 # full run (includes accuracy)
#   bash scripts/run_profiler.sh --skip-accuracy # fast smoke test
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# --- work on a WSL-native copy when running under WSL against /mnt/c ---------
# pip/venv operations on the 9p /mnt/c mount are pathologically slow (pip can
# stall for tens of minutes in D-state). If we're in WSL and HERE lives on
# /mnt/c, rsync the repo (minus .git/vendor/.venv) to $HOME and run there,
# then copy submission.json back.
WORK="$HERE"
copy_back=0
if grep -qi microsoft /proc/version 2>/dev/null && [[ "$HERE" == /mnt/* ]]; then
    WORK="$HOME/.kanzu-profiler-run"
    copy_back=1
    echo "WSL + Windows mount detected; using native copy at $WORK ..."
    rm -rf "$WORK"
    mkdir -p "$WORK"
    rsync -a --exclude '.git' --exclude 'vendor' --exclude '.venv' "$HERE/" "$WORK/"
    # Point the copy's .git at the real repo so `git rev-parse HEAD` (used by
    # the profiler's reproducibility block) resolves to the true commit SHA.
    printf 'gitdir: %s/.git\n' "$HERE" > "$WORK/.git"
fi
cd "$WORK"

EXTRA_ARGS=("$@")

# --- check model -------------------------------------------------------------
MODEL="$(python3 -c "import json;print(json.load(open('metadata.json'))['_runtime']['model_path'])" 2>/dev/null || echo model/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf)"
if [ ! -f "$MODEL" ]; then
    echo "Model not found at $MODEL. Run download_model.sh first."
    exit 1
fi

# --- set up venv -------------------------------------------------------------
VENV="$HERE/.venv"
if [ ! -d "$VENV" ]; then
    python3 -m venv "$VENV"
fi
# shellcheck disable=SC1091
source "$VENV/bin/activate"
pip install --quiet --upgrade pip

# --- locate profiler source (vendored copy preferred) ------------------------
PROFILER_DIR="$WORK/.tools/adtc-profiler"
if [ ! -d "$PROFILER_DIR" ]; then
    mkdir -p "$(dirname "$PROFILER_DIR")"
    echo "Cloning adtc-profiler into $PROFILER_DIR ..."
    git clone --depth 1 https://github.com/Africa-Deep-Tech-Foundation/adtc-profiler "$PROFILER_DIR"
fi

if pip install --quiet -e "$PROFILER_DIR" 2>/dev/null; then
    PROTO=installed
else
    # Editable install failed (e.g. no network); fall back to PYTHONPATH on the
    # vendored sources plus the runtime deps the profiler needs.
    pip install --quiet click rich psutil pyyaml jsonschema || true
    export PYTHONPATH="$PROFILER_DIR/src${PYTHONPATH:+:$PYTHONPATH}"
    PROTO=vendored
fi

# --- run ---------------------------------------------------------------------
echo "Running ADTC profiler ($PROTO) against metadata.json ..."
python -m adtc_profiler.cli run \
    --submission "$WORK" \
    --mode participant \
    --output "$WORK/submission.json" \
    "${EXTRA_ARGS[@]}"

# --- copy result back to the original repo when we ran on a native copy ------
if [ "$copy_back" = 1 ]; then
    cp "$WORK/submission.json" "$HERE/submission.json"
    echo "Copied submission.json back to $HERE"
fi

echo "Output: $HERE/submission.json"
echo "Size:   $(wc -c < "$HERE/submission.json") bytes"

# --- validate ----------------------------------------------------------------
python - <<'PYEOF'
import json, sys
try:
    report = json.load(open("submission.json", encoding="utf-8"))
    meta = json.load(open("metadata.json", encoding="utf-8"))
    sub = report["submission"]
    for key in ("team_id", "domain", "language_scope"):
        if sub.get(key) != meta.get(key):
            print(f"WARN: submission.{key} != metadata.{key}", file=sys.stderr)
    print("submission.json parsed; submission.* claims match metadata.json")
except Exception as exc:  # noqa: BLE001
    print(f"ERROR validating submission.json: {exc}", file=sys.stderr)
    sys.exit(1)
PYEOF
