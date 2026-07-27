#!/usr/bin/env python3
"""Generates the 橘粒 Juli brand OG card (1200×630) used as og:image.

Maximally bold for WeChat's small thumbnail: solid orange + HUGE "橘粒"
(340px white) so it's legible even at ~100px thumbnail width.

Run: python3 cmd/gui/assets/gen-og-image.py → scp to julia:/home/juli/web/og/
"""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

W, H = 1200, 630
OUT = Path(__file__).parent / "makro-og.png"

ORANGE = "#D97C26"
ORANGE_DEEP = "#B8681A"
WHITE = "#FFFFFF"
CREAM = "#FFF8F0"


def font(size, path="/System/Library/Fonts/PingFang.ttc", idx=0):
    try:
        return ImageFont.truetype(path, size, index=idx)
    except Exception:
        try:
            return ImageFont.truetype(path, size)
        except Exception:
            return ImageFont.load_default()


def text_size(draw, text, f):
    bbox = draw.textbbox((0, 0), text, font=f)
    return bbox[2] - bbox[0], bbox[3] - bbox[1]


def main():
    img = Image.new("RGB", (W, H), ORANGE)
    d = ImageDraw.Draw(img)
    d.rectangle([0, H - 8, W, H], fill=ORANGE_DEEP)

    f_huge = font(340)  # 橘粒 — fills the card, legible at thumbnail size
    f_sub = font(80)    # Juli

    brand = "橘粒"
    tw, th = text_size(d, brand, f_huge)
    d.text(((W - tw) // 2, (H - th) // 2 - 70), brand, font=f_huge, fill=WHITE)

    sub = "Juli"
    sw, _ = text_size(d, sub, f_sub)
    d.text(((W - sw) // 2, H // 2 + 130), sub, font=f_sub, fill=CREAM)

    img.save(OUT)
    print(f"wrote {OUT} {img.size}")


if __name__ == "__main__":
    main()
