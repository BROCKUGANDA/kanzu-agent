#!/usr/bin/env bash
# Root-level entry point kept for the ADTC submission contract
# (REPORT.md documents `bash download_model.sh` from the repo root).
# The implementation lives in scripts/download_model.sh; this wrapper only
# forwards arguments so there is a single source of truth to maintain.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$HERE/scripts/download_model.sh" "$@"
