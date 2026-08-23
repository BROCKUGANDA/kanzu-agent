#!/usr/bin/env bash
set -e
export PATH="$HOME/.local/opt/llama.cpp/llama-b10593:$PATH"
SRC="/mnt/c/Users/HP/Desktop/kanzu agent"
DST="$HOME/kanzu-work"
rm -rf "$DST"
mkdir -p "$DST"
# copy everything except heavy vendor dirs we don't need for profiling; keep model, scripts, .tools, metadata etc.
rsync -a --exclude 'vendor' --exclude '.git' --exclude '.venv' "$SRC/" "$DST/"
cd "$DST"
bash scripts/run_profiler.sh --skip-accuracy 2>&1 | tail -80
