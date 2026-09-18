#!/usr/bin/env python3
"""Crop a photo/screenshot/logo region out of a rendered reference page.

Usage:
    python crop_ref_region.py <source.png> --pos center-right --share 0.3 \
        --out <project>/images/p25_shot.png [--pad 0.05]
    python crop_ref_region.py <source.png> --rx 0.05 --ry 0.30 --rw 0.45 --rh 0.55 --out crop.png

WHY: some reference regions (UI screenshots, photos, logos) cannot be redrawn
with the 6-shape SVG vocabulary — they used to be silently DROPPED, which QA
then flagged as missing content. Instead: crop the region out of the reference
page render (page-N.png) and embed it into the slide as an <image> element.

Positions are the VLM's qualitative LAYOUT judgments ("image regions (position:
center-right, rough size share 30%)") mapped onto a 3x3 grid; --share is the
page-AREA fraction (side = sqrt(share)). Crops are deliberately generous (--pad)
because VLM positions are approximate — for screenshots a slightly larger crop
beats a clipped one. Prints the normalized rect actually used so the SVG
<image> placement can match its aspect ratio; run embed_images.py afterwards
to inline the file into the SVG.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import sys

try:
    from console_encoding import configure_utf8_stdio
except ImportError:
    _here = os.path.dirname(os.path.abspath(__file__))
    if _here not in sys.path:
        sys.path.insert(0, _here)
    try:
        from console_encoding import configure_utf8_stdio
    except ImportError:
        configure_utf8_stdio = None

if configure_utf8_stdio is not None:
    configure_utf8_stdio()

# 3x3 grid anchors (normalized centers) for the VLM's qualitative positions.
GRID = {
    "top-left": (1 / 6, 1 / 6), "top": (0.5, 1 / 6), "top-right": (5 / 6, 1 / 6),
    "left": (1 / 6, 0.5), "center": (0.5, 0.5), "center-left": (1 / 6, 0.5),
    "center-right": (5 / 6, 0.5), "right": (5 / 6, 0.5),
    "bottom-left": (1 / 6, 5 / 6), "bottom": (0.5, 5 / 6), "bottom-right": (5 / 6, 5 / 6),
    "full": (0.5, 0.5),
}


def clamp(v, lo=0.0, hi=1.0):
    return max(lo, min(hi, v))


def main():
    ap = argparse.ArgumentParser(description="Crop an approximate region from a reference page PNG.")
    ap.add_argument("source", help="reference page render, e.g. ~/.hiq/pdf-pages/page-25.png")
    ap.add_argument("--pos", default="center",
                    help="qualitative position from the LAYOUT section (3x3 grid, e.g. top-left/center-right/full)")
    ap.add_argument("--share", type=float, default=0.2,
                    help="rough share of the PAGE AREA the region occupies (0-1); side = sqrt(share)")
    ap.add_argument("--rx", type=float, default=None, help="explicit normalized rect x (overrides --pos/--share)")
    ap.add_argument("--ry", type=float, default=None, help="explicit normalized rect y")
    ap.add_argument("--rw", type=float, default=None, help="explicit normalized rect width")
    ap.add_argument("--rh", type=float, default=None, help="explicit normalized rect height")
    ap.add_argument("--pad", type=float, default=0.05, help="expand the crop by this normalized fraction on each side")
    ap.add_argument("--out", required=True, help="output PNG path (project images/ dir)")
    args = ap.parse_args()

    if args.rx is not None and args.ry is not None and args.rw and args.rh:
        x, y, w, h = args.rx, args.ry, args.rw, args.rh
    else:
        pos = args.pos.lower().strip()
        if pos not in GRID:
            print(json.dumps({"error": "unknown --pos %r; try one of %s" % (args.pos, ", ".join(sorted(GRID)))}))
            return 1
        cx, cy = GRID[pos]
        if pos == "full":
            w = h = 1.0
        else:
            side = clamp(math.sqrt(clamp(args.share, 0.01, 1.0)) * 1.15, 0.08, 1.0)  # generous side
            w = h = side
            # regions are usually wider than tall on slides — bias landscape
            w = clamp(w * 1.25, w, 1.0)
        x, y = clamp(cx - w / 2), clamp(cy - h / 2)

    x, y = clamp(x - args.pad), clamp(y - args.pad)
    w, h = clamp(w + 2 * args.pad, 0.02, 1.0 - x), clamp(h + 2 * args.pad, 0.02, 1.0 - y)

    try:
        from PIL import Image
    except ImportError:
        print(json.dumps({"error": "Pillow not installed"}))
        return 1
    if not os.path.isfile(args.source):
        print(json.dumps({"error": "source not found: %s" % args.source}))
        return 1

    im = Image.open(args.source)
    W, H = im.size
    box = (round(x * W), round(y * H), round((x + w) * W), round((y + h) * H))
    crop = im.crop(box)
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    crop.save(args.out)
    print(json.dumps({
        "out": args.out, "size": [crop.size[0], crop.size[1]],
        "rect": {"x": round(x, 3), "y": round(y, 3), "w": round(w, 3), "h": round(h, 3)},
        "aspect": round(crop.size[0] / max(1, crop.size[1]), 3),
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
