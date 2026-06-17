#!/usr/bin/env python3
"""Generate Makro icon variants - different accent colors."""
from PIL import Image, ImageDraw, ImageFont
import os

SIZE = 1024
ACCENT_COLORS = {
    "white": (228, 228, 231),
}

font = None
for path in [
    "/System/Library/Fonts/NewYork.ttf",
    "/System/Library/Fonts/SFNSDisplay.ttf",
    "/System/Library/Fonts/Helvetica.ttc",
    "/Library/Fonts/Arial Bold.ttf",
]:
    if os.path.exists(path):
        try:
            font = ImageFont.truetype(path, 640)
            break
        except:
            continue
if font is None:
    font = ImageFont.load_default()

out_dir = os.path.join(os.path.dirname(__file__), "build")
os.makedirs(out_dir, exist_ok=True)

for name, color in ACCENT_COLORS.items():
    img = Image.new("RGBA", (SIZE, SIZE), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    draw.rounded_rectangle([0, 0, SIZE-1, SIZE-1], radius=210, fill=(20, 20, 24, 255))

    text = "M"
    bbox = draw.textbbox((0, 0), text, font=font)
    tw, th = bbox[2] - bbox[0], bbox[3] - bbox[1]
    x = (SIZE - tw) // 2 - bbox[0]
    y = (SIZE - th) // 2 - bbox[1] - 20
    draw.text((x, y), text, fill=color, font=font)

    p = os.path.join(out_dir, f"appicon.png")
    img.save(p, "PNG")
    print(f"{name}: {color}")

print("Done")
