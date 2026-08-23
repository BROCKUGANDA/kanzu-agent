#!/usr/bin/env bash
set -e
# clean the bad bashrc line (it captured windows PATH)
sed -i '/llama-b10593/d' ~/.bashrc
printf 'export PATH="$HOME/.local/opt/llama.cpp/llama-b10593:$PATH"\n' >> ~/.bashrc
B="$HOME/.local/opt/llama.cpp/llama-b10593"
ls -la "$B" | head
"$B/llama-bench" --version
echo VERIFY_PATH
bash -lc 'which llama-bench && llama-bench --version'
