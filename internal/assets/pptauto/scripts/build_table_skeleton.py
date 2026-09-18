#!/usr/bin/env python3
"""Build a slide SVG from the markdown tables already transcribed in page-N.json.

Usage:
    python build_table_skeleton.py ~/.hiq/pdf-pages/page-3.json \
        --title "中国移动智算黑龙江超万卡项目概述" \
        --lead "首个7000P智算集群…" \
        --out <project_dir>/svg_output/slide_03.svg

WHY: the analyzer's CONTENT section already transcribes reference tables as
markdown pipe tables (rows, columns, every cell verbatim). The model redrawing
those tables by hand is where fidelity died — sub-rows dropped, cells truncated
(QA's p3/p4 MAJOR class). This script turns the transcription into the slide
MECHANICALLY: grid, column widths (CJK-aware), text wrapping, header styling
and zebra striping are all computed, colors/fonts come from template_config.json
— the model never touches coordinates for table pages.

Fidelity contract: every cell of the markdown table lands in the SVG. If the
table is too tall, font size steps down (15 -> 10.5) before any truncation; a
still-overflowing table is rendered anyway and the JSON summary says so.

Output: a complete valid slide SVG (title + optional lead + tables stacked) +
a JSON summary on stdout (rows/cols per table, font size used, overflow flag).
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
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

CANVAS_W, CANVAS_H = 1280, 720
TITLE_H = 70          # reserved band for the page title
LEAD_H = 46           # per line of lead text
MARGIN_X = 50
TABLE_GAP = 26


def cjk_w(ch):
    """Approximate char width in font-size units (CJK ≈ 1.0, latin ≈ 0.55)."""
    return 1.0 if ord(ch) > 0x2E80 else 0.55


def text_width(s, fs):
    return sum(cjk_w(c) for c in s) * fs


def wrap_text(s, fs, max_units):
    """Greedy wrap: break at spaces when possible, else hard-break CJK runs.
    Returns list of lines, each ≤ max_units wide (single overflow chars kept)."""
    lines, cur, cur_w = [], "", 0.0
    for ch in s:
        w = cjk_w(ch)
        if cur_w + w > max_units and cur:
            if ch == " ":
                lines.append(cur)
                cur, cur_w = "", 0.0
                continue
            if " " in cur[-int(max_units * 0.4):] if max_units > 8 else False:
                # prefer breaking at the last space near the line end
                cut = cur.rfind(" ", int(len(cur) * 0.6))
                lines.append(cur[:cut])
                rest = cur[cut + 1:]
                cur, cur_w = rest, sum(cjk_w(c) for c in rest) * 1
            else:
                lines.append(cur)
                cur, cur_w = "", 0.0
        cur += ch
        cur_w += w
    if cur:
        lines.append(cur)
    return lines or [""]


def esc(s):
    return (s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
             .replace('"', "&quot;"))


def parse_markdown_tables(md):
    """Extract pipe tables from the CONTENT markdown -> list of row-lists.
    Tolerant of ragged markdown (the analyzer's double-header tables often have
    separator/continuation rows with different cell counts): contiguous pipe
    lines form ONE table; every row is padded to the block's max column count."""
    tables, cur = [], []
    for line in md.splitlines():
        t = line.strip()
        # The analyzer's markdown is inconsistent: some rows lead with a pipe,
        # many don't ("B14-T108 | 100% | 97%"). Prose with 2+ pipes is rare, so
        # accept any line that has at least two pipe separators.
        if t.count("|") >= 2 or (t.startswith("|") and t.count("|") >= 1):
            cells = [c.strip() for c in t.strip("|").split("|")]
            if all(re.fullmatch(r":?-{2,}:?", c or "-") for c in cells):
                continue  # separator row
            cur.append(cells)
        elif cur:
            tables.append(cur)
            cur = []
    if cur:
        tables.append(cur)
    out = []
    for tbl in tables:
        n = max(len(r) for r in tbl)
        out.append([r + [""] * (n - len(r)) for r in tbl])
    return out


def load_style(home):
    cfg = {}
    for cand in (os.path.join(home, ".hiq", "skills", "ppt-auto", "template_config.json"),):
        try:
            with open(cand, "r", encoding="utf-8") as f:
                cfg = json.load(f)
            break
        except (OSError, ValueError):
            pass
    colors = cfg.get("colors") or {}
    fonts = cfg.get("fonts") or {}
    return {
        "accent": colors.get("accent") or "#4472C4",
        "text": colors.get("text") or "#1A1A1A",
        "muted": colors.get("text_secondary") or "#666666",
        "line": colors.get("line") or "rgba(0,0,0,0.15)",
        "header_text": "#FFFFFF",
        "font": fonts.get("family") or '"Microsoft YaHei", sans-serif',
    }


def render_table(tbl, x, y, w, style, fs, min_row_h=24, pad=8):
    """Render one table into SVG parts. Returns (svg_strings, height_used, cols)."""
    ncols = max(len(r) for r in tbl)
    # column weight = max cell width per column (units), floor at 6 units
    weights = []
    for c in range(ncols):
        mw = 6.0
        for r in tbl:
            if c < len(r):
                mw = max(mw, sum(cjk_w(ch) for ch in r[c]))
        weights.append(mw)
    tot = sum(weights)
    col_w = [max(52.0, w * wt / tot) for wt in weights]
    scale = w / sum(col_w)
    col_w = [cw * scale for cw in col_w]

    line_h = fs * 1.35
    rows_svg, y_cur = [], y
    for ri, row in enumerate(tbl):
        # wrapped lines per cell
        wrapped = []
        for ci in range(ncols):
            cell = row[ci] if ci < len(row) else ""
            max_units = max(4.0, (col_w[ci] - 2 * pad) / fs)
            wrapped.append(wrap_text(cell, fs, max_units))
        row_h = max(min_row_h, max(len(ws) for ws in wrapped) * line_h + 2 * pad)
        # row background
        if ri == 0:
            rows_svg.append('<rect x="%d" y="%.1f" width="%d" height="%.1f" fill="%s"/>'
                            % (x, y_cur, w, row_h, style["accent"]))
        elif ri % 2 == 0:
            rows_svg.append('<rect x="%d" y="%.1f" width="%d" height="%.1f" fill="rgba(0,0,0,0.035)"/>'
                            % (x, y_cur, w, row_h))
        # cell borders (below each row) + verticals are avoided for a clean look
        rows_svg.append('<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1"/>'
                        % (x, y_cur + row_h, x + w, y_cur + row_h, style["line"]))
        # cell text
        cx = x
        for ci in range(ncols):
            fill = style["header_text"] if ri == 0 else style["text"]
            weight = ' font-weight="bold"' if ri == 0 else ""
            ty = y_cur + pad + fs
            for li, ln in enumerate(wrapped[ci]):
                rows_svg.append(
                    '<text x="%.1f" y="%.1f" font-size="%.1f" fill="%s"%s>%s</text>'
                    % (cx + pad, ty + li * line_h, fs, fill, weight, esc(ln)))
            cx += col_w[ci]
        y_cur += row_h
    return rows_svg, y_cur - y, ncols


def main():
    ap = argparse.ArgumentParser(description="Mechanically build a table slide from page-N.json CONTENT.")
    ap.add_argument("page_json", help="page-N.json whose description contains markdown tables")
    ap.add_argument("--out", required=True, help="output slide SVG path")
    ap.add_argument("--title", default="", help="page title text (drawn top-left per config rule)")
    ap.add_argument("--lead", default="", help="optional intro paragraph under the title")
    ap.add_argument("--rows", default=None,
                    help="keep only rows A-B (1-based inclusive) — use to SPLIT an "
                         "overflowing table across consecutive slides")
    ap.add_argument("--compact", action="store_true",
                    help="dense mode: tighter rows/padding and a deeper font ladder "
                         "(down to 9.5) — fits ~24 rows on one slide instead of splitting")
    ap.add_argument("--extra-md-file", default=None, dest="extra_md_file",
                    help="additional markdown file appended as MORE tables (e.g. a "
                         "hand-shaped timeline table saved next to the page json)")
    ap.add_argument("--home", default=None)
    args = ap.parse_args()

    home = args.home or os.path.expanduser("~")
    try:
        with open(args.page_json, "r", encoding="utf-8") as f:
            desc = json.load(f).get("description") or ""
    except (OSError, ValueError) as e:
        print(json.dumps({"error": "read %s: %s" % (args.page_json, e)}))
        return 1
    tables = parse_markdown_tables(desc)
    if args.extra_md_file:
        try:
            with open(args.extra_md_file, "r", encoding="utf-8") as f:
                tables += parse_markdown_tables(f.read())
        except OSError as e:
            print(json.dumps({"error": "read --extra-md-file: %s" % e}))
            return 1
    if args.rows:
        a, _, b = args.rows.partition("-")
        try:
            a, b = int(a), int(b or a)
            tables = [tbl[a - 1:b] for tbl in tables if len(tbl) >= a]
        except ValueError:
            print(json.dumps({"error": "bad --rows %r (want A-B)" % args.rows}))
            return 1
    if not tables:
        print(json.dumps({"error": "no markdown table found in %s" % args.page_json,
                          "hint": "table pages only; non-table pages use the normal flow"}))
        return 1

    style = load_style(home)
    avail_w = CANVAS_W - 2 * MARGIN_X
    top = TITLE_H if args.title else 24
    if args.lead:
        top += LEAD_H

    # font-size ladder: fit all tables into the canvas, else keep going (overflow flag)
    ladder = (13.5, 12.5, 11.5, 10.5, 10.0, 9.5) if args.compact else (15.0, 13.5, 12.5, 11.5, 10.5)
    chosen_fs, overflow = ladder[0], False
    for fs in ladder:
        chosen_fs = fs
        y = top
        ok = True
        for tbl in tables:
            _, h, _ = render_table(tbl, MARGIN_X, y, avail_w, style, fs,
                                min_row_h=19 if args.compact else 24,
                                pad=4 if args.compact else 8)
            y += h + TABLE_GAP
        if y <= CANVAS_H - 12:
            break
        overflow = y > CANVAS_H - 12 and fs == ladder[-1]
    else:
        overflow = True

    parts = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="%s">'
             % (CANVAS_W, CANVAS_H, esc(style["font"]))]
    if args.title:
        parts.append('<text x="60" y="46" font-size="26" font-weight="bold" fill="%s">%s</text>'
                     % (style["text"], esc(args.title)))
        parts.append('<rect x="60" y="58" width="60" height="3" fill="%s"/>' % style["accent"])
    if args.lead:
        for i, ln in enumerate(wrap_text(args.lead, 14, (avail_w - 20) / 14.0)):
            parts.append('<text x="60" y="%d" font-size="14" fill="%s">%s</text>'
                         % (TITLE_H + 10 + i * 20, style["muted"], esc(ln)))
    y = top
    stats = []
    for tbl in tables:
        svgs, h, ncols = render_table(tbl, MARGIN_X, y, avail_w, style, chosen_fs,
                                       min_row_h=19 if args.compact else 24,
                                       pad=4 if args.compact else 8)
        parts.extend(svgs)
        stats.append({"rows": len(tbl), "cols": ncols})
        y += h + TABLE_GAP
    parts.append("</svg>")

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write("\n".join(parts))
    print(json.dumps({"out": args.out, "tables": stats, "font_size": chosen_fs,
                      "overflow": overflow, "bottom_y": round(y),
                      "hint": "overflow: rerun with --rows A-B to split across slides"
                              if overflow else ""}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
