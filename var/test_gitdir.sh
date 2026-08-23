#!/usr/bin/env bash
set -e
SRC="/mnt/c/Users/HP/Desktop/kanzu agent"
DST="$HOME/.kanzu-profiler-run"
rm -rf "$DST"; mkdir -p "$DST"
rsync -a --exclude '.git' --exclude 'vendor' --exclude '.venv' "$SRC/" "$DST/"
printf 'gitdir: %s/.git\n' "$SRC" > "$DST/.git"
cd "$DST"
git rev-parse --short=12 HEAD && echo GITDIR_POINTER_OK || echo POINTER_FAILED
