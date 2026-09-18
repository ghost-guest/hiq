#!/usr/bin/env python3
"""Render each page of a PDF to a PNG image (no OCR, no text extraction).

This is the "split PDF into one image per page" step of hiq's
PDF → ppt-auto pipeline. A scanned / image-style PDF becomes a stack of
page images; the Go desktop layer (desktop/pdf_pages_vision.go) then sends
each PNG to builtin.CallVLM to recognize the page's TEXT + LAYOUT + FORMAT +
DESIGN, and ppt-auto redraws each page from that description.

Mirrors ocr_pdf.py's fitz rendering (same PyMuPDF dep, same Matrix scale) but
ONLY renders pixels — it does not run PaddleOCR or extract text. That keeps the
VLM (not OCR) as the recognizer, so layout/format/design come through, not just
text.

Usage:
    python pdf_to_page_images.py <pdf_path> <out_dir> [--scale 2.0] [--max-pages 0]

Outputs:
    <out_dir>/page-1.png, page-2.png, ...
    The LAST line of stdout is a JSON object: {"pages": ["<out_dir>/page-1.png", ...]}
    so the Go caller can parse it without globbing. Earlier lines are progress logs.
    On error, the JSON line is {"error": "..."} and exit code is 1.
"""
import argparse
import json
import os
import sys


def render_pages(pdf_path: str, out_dir: str, scale: float, max_pages: int):
    """Render up to max_pages pages of pdf_path into out_dir as PNGs.

    Returns the list of rendered file paths. Uses the same fitz.Matrix(scale,
    scale) as ocr_pdf.py's _ocr_page so DPI/quality match the existing PDF path.
    """
    import fitz  # PyMuPDF — already a dep via ocr_pdf.py

    os.makedirs(out_dir, exist_ok=True)
    doc = fitz.open(pdf_path)
    try:
        total = doc.page_count
        n = total if not max_pages or max_pages <= 0 else min(total, max_pages)
        mat = fitz.Matrix(scale, scale)
        paths = []
        for i in range(n):
            page = doc[i]
            pix = page.get_pixmap(matrix=mat)
            out = os.path.join(out_dir, f"page-{i + 1}.png")
            pix.save(out)
            paths.append(out)
            print(f"[pdf_to_page_images] rendered page {i + 1}/{n} -> {out}", file=sys.stderr)
        return paths, total
    finally:
        doc.close()


def main():
    ap = argparse.ArgumentParser(description="Render PDF pages to PNGs (no OCR).")
    ap.add_argument("pdf_path", help="Path to the input .pdf")
    ap.add_argument("out_dir", help="Directory to write page-N.png files into")
    ap.add_argument("--scale", type=float, default=2.0,
                    help="Render scale (2.0 = ~144 DPI; matches ocr_pdf.py)")
    ap.add_argument("--max-pages", type=int, default=0,
                    help="Cap pages rendered (0 = all). Useful for huge PDFs.")
    args = ap.parse_args()

    if not os.path.isfile(args.pdf_path):
        print(json.dumps({"error": f"not a file: {args.pdf_path}"}))
        sys.exit(1)

    try:
        paths, total = render_pages(args.pdf_path, args.out_dir, args.scale, args.max_pages)
        # Last stdout line = machine-readable JSON for the Go caller.
        print(json.dumps({"pages": paths, "total_pages": total, "rendered": len(paths)}))
    except Exception as e:  # surfaced to the Go caller via the JSON line
        print(json.dumps({"error": f"{type(e).__name__}: {e}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
