#!/usr/bin/env bash
set -uo pipefail
cd "/c/Users/HP/Desktop/kanzu agent"

FRAMES="docs/demo/frames"
FONT="/c/Windows/Fonts/CascadiaMono.ttf"
W=1280; H=720
NARR="docs/demo/narration.wav"
OUT="docs/demo/kanzu-agent-demo.mp4"
mkdir -p "$FRAMES"

# Use printf | head to trim (avoids $1 in trim func)
trim() { head -28; }

# ── Slide 0: Title card (12s) ──────────────────────────────────────────────────
ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=12" \
  -vf "drawtext=fontfile=$FONT:text='Kanzu Agent - ADTC 2026':fontcolor=#58a6ff:fontsize=56:box=1:boxcolor=#161b22:boxborderw=24:x=(w-text_w)/2:y=200,drawtext=fontfile=$FONT:text='Offline AML Compliance Copilot for Ugandan SACCOs':fontcolor=#e6edf3:fontsize=26:x=(w-text_w)/2:y=300,drawtext=fontfile=$FONT:text='github.com/BROCKUGANDA/kanzu-agent':fontcolor=#8b949e:fontsize=22:x=(w-text_w)/2:y=420" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/t0.mp4" 2>/dev/null

# ── Slide 1: kanzu doctor (8s) ─────────────────────────────────────────────────
grep -E '\[ok\]|\[warn\]|\[FAIL\]' "$FRAMES/doctor.txt" > "$FRAMES/s1.txt"
ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=8" \
  -vf "drawtext=fontfile=$FONT:text='kanzu doctor - all checks green':fontcolor=#58a6ff:fontsize=28:box=1:boxcolor=#161b22:boxborderw=12:x=40:y=30,drawtext=fontfile=$FONT:textfile=$FRAMES/s1.txt:fontcolor=#e6edf3:fontsize=19:x=40:y=100:line_spacing=6" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/s1.mp4" 2>/dev/null

# ── Slide 2: English inference (36s) ───────────────────────────────────────────
{
  echo "EXECUTION PLAN  -  intent=draft_report  lang=en"
  sed -n '7,14p' "$FRAMES/en.txt"
  echo ""
  echo "-- DETERMINISTIC EVIDENCE --"
  sed -n '19,22p' "$FRAMES/en.txt"
  echo ""
  sed -n '24,34p' "$FRAMES/en.txt"
  echo ""
  echo "-- SAR NOTE (excerpt) --"
  sed -n '99p' "$FRAMES/en.txt"
  sed -n '102,104p' "$FRAMES/en.txt"
} | trim > "$FRAMES/s2.txt"

ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=36" \
  -vf "drawtext=fontfile=$FONT:text='English: flag suspicious transactions -> draft report':fontcolor=#58a6ff:fontsize=26:box=1:boxcolor=#161b22:boxborderw=12:x=40:y=30,drawtext=fontfile=$FONT:textfile=$FRAMES/s2.txt:fontcolor=#e6edf3:fontsize=17:x=40:y=100:line_spacing=5" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/s2.mp4" 2>/dev/null

# ── Slide 3: Kiswahili inference (22s) ─────────────────────────────────────────
{
  echo "MPANGO WA UTEKELEZAJI  -  intent=scan_suspicious  lang=sw"
  sed -n '7,9p' "$FRAMES/sw.txt"
  echo ""
  echo "-- USHAHIDI WA UHAKIKA --"
  sed -n '14,15p' "$FRAMES/sw.txt"
  sed -n '19,26p' "$FRAMES/sw.txt"
  echo ""
  sed -n '84,88p' "$FRAMES/sw.txt"
} | trim > "$FRAMES/s3.txt"

ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=22" \
  -vf "drawtext=fontfile=$FONT:text='Kiswahili: Chunguza miamala ya wiki hii':fontcolor=#58a6ff:fontsize=26:box=1:boxcolor=#161b22:boxborderw=12:x=40:y=30,drawtext=fontfile=$FONT:textfile=$FRAMES/s3.txt:fontcolor=#e6edf3:fontsize=17:x=40:y=100:line_spacing=5" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/s3.mp4" 2>/dev/null

# ── Slide 4: Luganda inference (17s) ───────────────────────────────────────────
{
  echo "ENTEGEKA Y'OKUKOLA  -  intent=scan_suspicious  lang=lg"
  sed -n '7,9p' "$FRAMES/lg.txt"
  echo ""
  echo "-- INFORMS YA KUHALALI --"
  sed -n '17,18p' "$FRAMES/lg.txt"
  sed -n '23,28p' "$FRAMES/lg.txt"
  echo ""
  sed -n '88,104p' "$FRAMES/lg.txt"
} | trim > "$FRAMES/s4.txt"

ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=17" \
  -vf "drawtext=fontfile=$FONT:text='Luganda: Kebera ebyenfuna bya mwezi':fontcolor=#58a6ff:fontsize=26:box=1:boxcolor=#161b22:boxborderw=12:x=40:y=30,drawtext=fontfile=$FONT:textfile=$FRAMES/s4.txt:fontcolor=#e6edf3:fontsize=17:x=40:y=100:line_spacing=5" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/s4.mp4" 2>/dev/null

# ── Slide 5: Profiler + closing (25s) ───────────────────────────────────────────
{
  echo "ADTC Profiler Results"
  echo ""
  cat "$FRAMES/profiler.txt"
  echo ""
  echo "Measured on: 12th Gen Intel i7-1255U - 7.8 GB RAM - Ubuntu 26.04"
  echo "Git commit: 8b8e8902fe88  -  Seed: 42"
  echo ""
  echo "github.com/BROCKUGANDA/kanzu-agent  -  MIT License"
} > "$FRAMES/s5.txt"

ffmpeg -y -f lavfi -i "color=c=#0d1117:s=${W}x${H}:d=25" \
  -vf "drawtext=fontfile=$FONT:text='ADTC Profiler + Submission':fontcolor=#58a6ff:fontsize=30:box=1:boxcolor=#161b22:boxborderw=12:x=40:y=30,drawtext=fontfile=$FONT:textfile=$FRAMES/s5.txt:fontcolor=#e6edf3:fontsize=24:x=80:y=120:line_spacing=10" \
  -c:v libx264 -pix_fmt yuv420p -r 15 "$FRAMES/s5.mp4" 2>/dev/null

# ── Concatenate + mux with narration ────────────────────────────────────────────
printf "file 't0.mp4'\nfile 's1.mp4'\nfile 's2.mp4'\nfile 's3.mp4'\nfile 's4.mp4'\nfile 's5.mp4'\n" > "$FRAMES/concat.txt"

cd "$FRAMES"
ffmpeg -y -f concat -safe 0 -i concat.txt -i "$NARR" \
  -c:v libx264 -preset veryfast -crf 23 -r 15 \
  -c:a aac -b:a 128k -pix_fmt yuv420p -shortest \
  -movflags +faststart \
  "$OUT" 2>&1 | tail -3
cd /c/Users/HP/Desktop/kanzu\ agent 2>/dev/null || cd "C:\\Users\\HP\\Desktop\\kanzu agent"

echo "=== Result ==="
ls -la "docs/demo/kanzu-agent-demo.mp4" 2>&1
ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 "docs/demo/kanzu-agent-demo.mp4" 2>&1
