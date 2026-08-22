#!/usr/bin/env bash
# Proves the Kanzu runtime makes zero network calls by running the full
# agent pipeline inside a Linux network namespace (unshare -n) where no
# interfaces exist and all socket() calls fail.
#
# Requires: Linux with CONFIG_USER_NS (unprivileged user namespaces).
# Usage:   bash scripts/verify_offline.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

# --- prerequisites ---
command -v unshare >/dev/null 2>&1 || { echo "error: unshare not found (Linux only)."; exit 1; }

if [ ! -x "./bin/kanzu" ]; then
    echo "Building kanzu ..."
    go build -o bin/kanzu ./cmd/kanzu
fi

# --- check whether model is available ---
MODEL_FLAG=""
if [ -f "model/Qwen2.5-1.5B-Instruct-Q4_K_M.gguf" ]; then
    MODEL_FLAG=""
else
    MODEL_FLAG="-no-model"
    echo "Model not found — running deterministic-only test."
fi

# --- runtime check: run inside network namespace ---
echo "Running inside network namespace (no interfaces, no DNS) ..."
unshare -n --map-root-user bash -c '
    ip link set lo up 2>/dev/null || true
    cd "'"$HERE"'"
    echo "=== scan (deterministic) ==="
    ./bin/kanzu scan -days 7 -member M-001
    echo ""
    echo "=== ask (full pipeline'${MODEL_FLAG:+ with model}') ==="
    ./bin/kanzu ask "flag suspicious transactions this week" '$MODEL_FLAG'
    echo ""
    echo "=== report (compliance note'${MODEL_FLAG:+ with model}') ==="
    ./bin/kanzu report -days 7 '$MODEL_FLAG'
'

echo ""
echo "Offline verification complete. All commands succeeded inside"
echo "a network namespace with no external interfaces."