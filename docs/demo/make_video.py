#!/usr/bin/env python3
"""Build demo video: slides timed to narration (76.58s)."""
import os, subprocess
import numpy as np
from PIL import Image, ImageDraw, ImageFont
import imageio
import imageio_ffmpeg

FRAMES = r"C:\Users\HP\Desktop\kanzu agent\docs/demo/frames"
NARR = r"C:\Users\HP\Desktop\kanzu agent\docs/demo/narration.wav"
OUT = r"C:\Users\HP\Desktop\kanzu agent\docs/demo/kanzu-agent-demo.mp4"
FONT_PATH = r"C:\Windows\Fonts/CascadiaMono.ttf"
W, H = 1280, 720
FPS = 15

FONT_BIG = ImageFont.truetype(FONT_PATH, 48)
FONT_TITLE = ImageFont.truetype(FONT_PATH, 32)
FONT_TEXT = ImageFont.truetype(FONT_PATH, 19)
FONT_SMALL = ImageFont.truetype(FONT_PATH, 16)

def read_trim(path, max_lines=24):
    full = os.path.join(FRAMES, path) if not os.path.isabs(path) else path
    with open(full, encoding="utf-8") as f:
        lines = [l for l in f.read().splitlines() if l.strip()]
    return lines[:max_lines]

def render_slide(duration, bg="#0d1117", title=None, body_lines=None,
                 title_size=32, body_size=19, title_y=30, body_y=100, line_spacing=6):
    nframes = round(duration * FPS)
    font_t = ImageFont.truetype(FONT_PATH, title_size) if title else None
    font_b = ImageFont.truetype(FONT_PATH, body_size)
    frames = []
    for _ in range(nframes):
        img = Image.new("RGB", (W, H), bg)
        d = ImageDraw.Draw(img)
        if title:
            bbox = font_t.getbbox(title)
            th = bbox[3] - bbox[1]
            d.rectangle([40-8, title_y-8, W-16, title_y + th + 16], fill="#161b22")
            d.text((40, title_y), title, fill="#58a6ff", font=font_t)
        if body_lines:
            y = body_y
            for line in body_lines:
                d.text((40, y), line, fill="#e6edf3", font=font_b)
                y += body_size + line_spacing
        frames.append(np.array(img))
    return frames

def render_title(duration):
    nframes = round(duration * FPS)
    frames = []
    for _ in range(nframes):
        img = Image.new("RGB", (W, H), "#0d1117")
        d = ImageDraw.Draw(img)
        d.text((W//2, 200), "Kanzu Agent - ADTC 2026", fill="#58a6ff", font=FONT_BIG, anchor="mm")
        d.text((W//2, 300), "Offline AML Compliance Copilot for Ugandan SACCOs", fill="#e6edf3", font=FONT_TITLE, anchor="mm")
        d.text((W//2, 420), "github.com/BROCKUGANDA/kanzu-agent", fill="#8b949e", font=FONT_SMALL, anchor="mm")
        frames.append(np.array(img))
    return frames

# Narration is 76.58s. Slide timeline (sum = 76s, video trimmed to 76.5s by ffmpeg):
#   0:00-0:10  Title (10s)
#   0:10-0:18  Doctor (8s)
#   0:18-0:44  English (26s)
#   0:44-0:59  Kiswahili (15s)
#   0:59-1:11  Luganda (12s)
#   1:11-1:16  Profiler summary (5s)
segments = [
    (10, render_title(10)),
    (8,  render_slide(8, title="kanzu doctor - all checks green",
                      body_lines=[l for l in read_trim("doctor.txt") if any(k in l for k in ("[ok]","[warn]","[FAIL]"))][:18])),
    (26, render_slide(26, title="English: flag suspicious transactions",
                      body_lines=read_trim("en.txt", 20))),
    (15, render_slide(15, title="Kiswahili: Chunguza miamala ya wiki hii",
                      body_lines=read_trim("sw.txt", 18))),
    (12, render_slide(12, title="Luganda: Kebera ebyenfuna bya mwezi",
                      body_lines=read_trim("lg.txt", 18))),
    (5,  render_slide(5, title="ADTC Profiler: 12.58 tok/s, 0.76 acc",
                      body_lines=["throughput: 12.58 tok/s", "accuracy: 0.76 (arc_easy)", "seed: 42", "commit: 8b8e8902fe88", "github.com/BROCKUGANDA/kanzu-agent"])),
]

all_frames = []
for dur, frames in segments:
    print(f"  slide {len(all_frames)//FPS:02d}s: {len(frames)} frames ({dur}s)")
    all_frames.extend(frames)

total_s = len(all_frames) / FPS
print(f"Total: {len(all_frames)} frames ({total_s:.1f}s)")

ffmpeg_path = imageio_ffmpeg.get_ffmpeg_exe()
vid_only = OUT.replace(".mp4", "_novid.mov")

writer = imageio.get_writer(vid_only, fps=FPS, codec='libx264', quality=8)
for frame in all_frames:
    writer.append_data(frame)
writer.close()
print("Video track written.")

cmd = [ffmpeg_path, "-y", "-i", vid_only, "-i", NARR,
       "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-r", "15",
       "-c:a", "aac", "-b:a", "128k", "-shortest", "-movflags", "+faststart",
       "-t", "76.5",
       OUT]
r = subprocess.run(cmd, capture_output=True, text=True)
os.remove(vid_only)
if r.returncode != 0:
    print("FFMPEG ERR:", r.stderr[-600:])
else:
    r2 = subprocess.run([ffmpeg_path, "-i", OUT], capture_output=True, text=True)
    for line in r2.stderr.splitlines():
        if any(k in line for k in ("Duration","Stream")):
            print(line)
    print(f"SUCCESS: {os.path.getsize(OUT)//1024}KB")
