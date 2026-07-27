#!/usr/bin/env python3
"""Generates the 橘粒 Juli brand OG card (1200×630) used as og:image for
shared artifact link previews (WeChat/Twitter cards).

Run: python3 cmd/gui/assets/gen-og-image.py
Outputs makro-og.png next to this script. Upload to OSS:
    aliyun oss cp makro-og.png oss://juli-makro/assets/makro-og.png
"""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

W, H = 1200, 630
OUT = Path(__file__).parent / "makro-og.png"


def font(size, path="/System/Library/Fonts/PingFang.ttc", idx=0):
    try:
        return ImageFont.truetype(path, size, index=idx)
    except Exception:
        try:
            return ImageFont.truetype(path, size)
        except Exception:
            return ImageFont.load_default()


def main():
    img = Image.new("RGB", (W, H), "#FAFAF8")
    d = ImageDraw.Draw(img)
    f_brand = font(120)
    f_kick = font(34)
    f_sub = font(52)
    f_mono = font(30, "/System/Library/Fonts/Menlo.ttc", 0)

    # 左侧橘色竖条(品牌点缀)
    d.rectangle([0, 0, 14, H], fill="#D97C26")
    # 橘色圆点 + 橘粒 Juli wordmark
    d.ellipse([92, 168, 128, 204], fill="#D97C26")
    d.text((148, 150), "橘粒 Juli", font=f_brand, fill="#1A1A1A")
    # kicker
    d.text((94, 312), "// MAKRO  ARTIFACT", font=f_kick, fill="#D97C26")
    # 副标题
    d.text((94, 372), "由 Makro 生成的报告", font=f_sub, fill="#5C5A54")
    # 底部 mono 行
    d.text((94, 548), "Powered by Makro™  ·  share.juliasia.cn", font=f_mono, fill="#8E8C84")

    img.save(OUT)
    print(f"wrote {OUT} {img.size}")


if __name__ == "__main__":
    main()
