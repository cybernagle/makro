#!/usr/bin/env python3
"""Generates the 橘粒 Juli brand OG card (1200×630) used as og:image for
shared artifact link previews (WeChat/Twitter cards).

Bold high-contrast design (solid orange + big white wordmark) so it's legible
at WeChat's small thumbnail size — a light bg + fine text reads as blank.

Run: python3 cmd/gui/assets/gen-og-image.py
Outputs makro-og.png next to this script. Upload to julia ECS:
    scp makro-og.png julia:/home/juli/web/og/makro-og.png
"""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

W, H = 1200, 630
OUT = Path(__file__).parent / "makro-og.png"

# 橘粒 brand palette
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
    img = Image.new("RGB", (W, H), ORANGE)  # solid brand orange — bold at any size
    d = ImageDraw.Draw(img)

    # subtle deeper-orange band at the bottom (depth, still high-contrast)
    d.rectangle([0, H - 8, W, H], fill=ORANGE_DEEP)

    f_brand = font(168)   # 橘粒 Juli (big, white, centered)
    f_kick = font(34)     # // MAKRO ARTIFACT
    f_sub = font(36)      # tagline

    # big white wordmark, centered
    brand = "橘粒 Juli"
    tw, th = text_size(d, brand, f_brand)
    d.text(((W - tw) // 2, (H - th) // 2 - 50), brand, font=f_brand, fill=WHITE)

    # kicker above the wordmark
    kick = "// MAKRO  ARTIFACT"
    kw, _ = text_size(d, kick, f_kick)
    d.text(((W - kw) // 2, (H // 2) - 140), kick, font=f_kick, fill=CREAM)

    # tagline below
    tag = "由 Makro™ 生成的报告"
    tw2, _ = text_size(d, tag, f_sub)
    d.text(((W - tw2) // 2, (H // 2) + 90), tag, font=f_sub, fill=CREAM)

    img.save(OUT)
    print(f"wrote {OUT} {img.size}")


if __name__ == "__main__":
    main()
