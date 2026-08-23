#!/usr/bin/env python3
"""Build kanzu-agent-demo.mp4 using imageio (bundled ffmpeg) + Pillow."""
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
FONT_24 = ImageFont.truetype(FONT_PATH, 24)

def read_trim(path, max_lines=24):
    with open(path, encoding="utf-8") as f:
        lines = [l for l in f.read().splitlines() if l.strip()]
    return lines[:max_lines]

def render_slide(duration, bg="#0d1117", title=None, body_lines=None,
                 title_size=32, body_size=19, title_y=30, body_y=100, line_spacing=6):
    nframes = duration * FPS
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
    frames = []
    for _ in range(duration * FPS):
        img = Image.new("RGB", (W, H), "#0d1117")
        d = ImageDraw.Draw(img)
        d.text((W//2, 200), "Kanzu Agent - ADTC 2026", fill="#58a6ff", font=FONT_BIG, anchor="mm")
        d.text((W//2, 300), "Offline AML Compliance Copilot for Ugandan SACCOs", fill="#e6edf3", font=FONT_TITLE, anchor="mm")
        d.text((W//2, 420), "github.com/BROCKUGANDA/kanzu-agent", fill="#8b949e", font=FONT_SMALL, anchor="mm")
        frames.append(np.array(img))
    return frames

# Build segments with timestamps for narration sync
segments = [
    (12, render_title(12)),
    (8,  render_slide(8, title="kanzu doctor - all checks green",
                       body_lines=[l for l in read_trim("doctor.txt") if any(k in l for k in ("[ok]","[warn]","[FAIL]"))][:20])),
    (36, render_slide(36, title="English: flag suspicious transactions -> draft report",
                      body_lines=read_trim("en.txt", 26))),
    (22, render_slide(22, title="Kiswahili: Chunguza miamala ya wiki hii",
                      body_lines=read_trim("sw.txt", 24))),
    (17, render_slide(17, title="Luganda: Kebera ebyenfuna bya mwezi",
                      body_lines=read_trim("lg.txt", 24))),
    (25, render_slide(25, title="ADTC Profiler + Submission",
                      body_lines=["ADTC Profiler Results", ""] + read_trim("profiler.txt") + [
                          "Measured on: 12th Gen Intel i7-1255U - 7.8 GB RAM - Ubuntu 26.04",
                          "Git commit: 8b8e8902fe88  -  Seed: 42",
                          "github.com/BROCKUGANDA/kanzu-agent  -  MIT License"],
                      body_size=24, body_y=120, line_spacing=10)),
]

all_frames = []
for dur, frames in segments:
    print(f"  slide {len(all_frames)//FPS:03d}s: {len(frames)} frames")
    all_frames.extend(frames)

print(f"Total: {len(all_frames)} frames ({len(all_frames)/FPS:.1f}s)")

# Write video + audio in a single ffmpeg call via raw pipe (no drawtext segfault)
ffmpeg_path = imageio_ffmpeg.get_ffmpeg_exe()
vid_only = OUT.replace(".mp4", "_novid.mp4")

# Write video-only MP4 via imageio
import imageio
writer = imageio.get_writer(vid_only, fps=FPS, codec='libx264', quality=8)
for frame in all_frames:
    writer.append_data(frame)
writer.close()
print("Video track written.")

# Step 2: add audio with ffmpeg
cmd = [ffmpeg_path, "-y", "-i", vid_only, "-i", NARR,
       "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
       "-c:a", "aac", "-b:a", "128k", "-shortest", "-movflags", "+faststart",
       OUT]
r = subprocess.run(cmd, capture_output=True, text=True)
os.remove(vid_only)
if r.returncode != 0:
    print("FFMPEG ERR:", r.stderr[-800:])
else:
    print(f"SUCCESS: {os.path.getsize(OUT)//1024}KB")
