#!/usr/bin/env bash
set -e
export PATH="$HOME/.local/opt/llama.cpp/llama-b10593:$PATH"
which llama-bench llama-cli
bash "/mnt/c/Users/HP/Desktop/kanzu agent/scripts/run_profiler.sh" --seed 42 2>&1 | tail -50
