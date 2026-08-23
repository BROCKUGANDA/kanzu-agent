#!/usr/bin/env bash
# Records the ADTC 2026 demo video for Kanzu Agent.
# Captures the terminal running kanzu commands, then muxes with TTS narration.
cd "/c/Users/HP/Desktop/kanzu agent"
set -e

WORK="$HERE/var/demo-work"
rm -rf "$WORK" && mkdir -p "$WORK"

# --- 1. Capture terminal session as a script for precise timing -------------
# We use script( to record a typescript of the terminal, running each kanzu
# command. The output is played back at a fixed rate so the video is clean.
SESSION="$WORK/session.log"

# --- 2. Run the demo commands, logging each with a banner for the captions ---
{
  echo "=== KANZU AGENT — ADTC 2026 DEMO ==="
  echo "[0:00] Running kanzu doctor..."
  ./bin/kanzu.exe doctor
  echo "=== END DOCTOR ==="
  echo "[0:20] English: flag suspicious transactions and draft compliance note..."
  ./kanzu.exe ask -lang en "Flag suspicious transactions from last week and draft a compliance note for the savings committee."
  echo "=== END EN ==="
  echo "[0:50] Kiswahili: chunguza miamala ya wiki hii..."
  ./kanzu.exe ask -lang sw "Chunguza miamala ya wiki hii."
  echo "=== END SW ==="
  echo "[1:15] Luganda: kebera ebyenfuna bya mwezi..."
  ./kanzu.exe ask -lang lg "Kebera ebyenfuna bya mwezi."
  echo "=== END LG ==="
  echo "[1:30] Profiler summary..."
  python -c "import json; d=json.load(open('submission.json')); print('throughput:', d['throughput']['tokens_per_second_generation'], 'tok/s'); print('accuracy:', d['accuracy'][0]['score'])"
  echo "=== END PROFILER ==="
} | tee "$SESSION" >/dev/null 2>&1 &

# --- 3. While commands run, set up ffmpeg to capture a 1280x720 region -------
# We'll capture the Windows terminal window via gdigrab and pipe the session
# log as an overlay subtitle track timed to the narration.
# For now, capture full-screen; the terminal is the active window.
ffmpeg -y -f gdigrab -framerate 15 -offset_x 0 -offset_y 0 -video_size 1280x720 \
  -t 125 -i title="kanzu-agent" \
  -i "$HERE/docs/demo/narration.wav" \
  -c:v libx264 -preset veryfast -crf 24 \
  -c:a aac -b:a 128k \
  -pix_fmt yuv420p \
  "$HERE/docs/demo/kanzu-agent-demo.mp4"
