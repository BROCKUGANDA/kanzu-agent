#!/usr/bin/env bash
# Builds llama.cpp CLI (and train-text-lora) into vendor/llama.cpp/build/bin/.
# Idempotent: skips clone if vendor/llama.cpp already exists.
# Usage: bash scripts/setup_llama_cpp.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$HERE/vendor/llama.cpp"
BUILD="$SRC/build"

# --- toolchain checks ---
command -v cmake >/dev/null 2>&1 || { echo "error: cmake not found. Install cmake >= 3.14."; exit 1; }
command -v make >/dev/null 2>&1 || command -v ninja >/dev/null 2>&1 || { echo "error: no build tool (make or ninja) found."; exit 1; }
command -v git >/dev/null 2>&1 || { echo "error: git not found."; exit 1; }

# --- clone (shallow) ---
if [ ! -d "$SRC" ]; then
    echo "Cloning llama.cpp into $SRC ..."
    git clone --depth 1 https://github.com/ggml-org/llama.cpp "$SRC"
fi

# --- configure ---
CMAKE_GEN=""
if command -v ninja >/dev/null 2>&1; then
    CMAKE_GEN="-G Ninja"
fi

mkdir -p "$BUILD"
cmake -S "$SRC" -B "$BUILD" $CMAKE_GEN \
    -DCMAKE_BUILD_TYPE=Release \
    -DLLAMA_CURL=OFF \
    -DLLAMA_BUILD_SERVER=OFF \
    -DLLAMA_BUILD_EXAMPLES=ON \
    -DLLAMA_BUILD_TESTS=OFF

# --- build ---
cmake --build "$BUILD" --target llama-cli train-text-lora -j "$(nproc 2>/dev/null || echo 4)"

echo "Build complete: $BUILD/bin/llama-cli"