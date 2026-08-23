#!/usr/bin/env bash
set -e
cd ~
URL="https://github.com/ggml-org/llama.cpp/releases/download/b10593/llama-b10593-bin-ubuntu-x64.tar.gz"
curl -sL "$URL" -o llama.tar.gz
mkdir -p ~/.local/opt/llama.cpp
tar xzf llama.tar.gz -C ~/.local/opt/llama.cpp
find ~/.local/opt/llama.cpp -maxdepth 2 \( -name "llama-bench" -o -name "llama-cli" \)
~/.local/opt/llama.cpp/build/bin/llama-bench --version 2>/dev/null || ~/.local/opt/llama.cpp/llama-bench --version 2>&1 | head -2
