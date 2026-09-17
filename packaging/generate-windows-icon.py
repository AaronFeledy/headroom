#!/usr/bin/env python3
"""Regenerate the Windows ICO from the shared SVG, retaining alpha transparency.

Requires CairoSVG and Pillow. Generated assets are committed; ordinary builds
and package tests do not require either library.
"""
from io import BytesIO
from pathlib import Path

import cairosvg
from PIL import Image

ROOT = Path(__file__).resolve().parents[1]
SIZES = (16, 24, 32, 48, 64, 128, 256)


def main():
    source = ROOT / "clients/desktop/headroom.svg"
    # Render each size directly, avoiding resampling halos in transparent corners.
    images = [Image.open(BytesIO(cairosvg.svg2png(url=str(source),
              output_width=size, output_height=size))).convert("RGBA") for size in SIZES]
    images[-1].save(ROOT / "clients/desktop/windows/headroom.ico", format="ICO",
                    append_images=images[:-1], sizes=[(size, size) for size in SIZES])


if __name__ == "__main__":
    main()
