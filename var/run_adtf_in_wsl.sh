#!/usr/bin/env bash
set -e
export PATH="$HOME/.local/opt/llama.cpp/llama-b10593:$PATH"
which llama-bench llama-cli
cd "/mnt/c/Users/HP/Desktop/kanzu agent"
# ensure python3 + venv available
python3 --version || sudo apt-get -y install python3
bash scripts/run_profiler.sh "$@" 2>&1 | tail -60
