#!/usr/bin/env python3
"""Build a flowchart / timeline slide SVG from a small DSL — no model coordinates.

Usage (flowchart):
    python build_flow_skeleton.py flow.dsl --title "..." --out slide_29.svg
Usage (timeline from a 2-col markdown table):
    python build_flow_skeleton.py --timeline --from-table timeline.md --title "..." --out slide_04.svg

DSL — nodes appear inline in edges (defined on first use), kinds by bracket:
    [process]   rounded rect
    {decision?} diamond
    (terminal)  pill (start/end)
Edges:
    [A] -> [B]                plain
    [A] -> {ok?} |是|         labelled
Comments start with #. Levels are computed as the longest path from a source;
nodes in the same level share a row; arrows are elbow polylines with heads.
Colors/fonts come from template_config.json (same authority chain as tables).

WHY: reference flowcharts died as "flat disconnected grids" (QA's p29 class) —
the model cannot hand-place connected diagrams. Here the model (or a human)
only declares nodes and edges; geometry, elbows and arrowheads are mechanical.
"""

from __future__ import annotations

import argparse
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
NODE_MIN_W, NODE_H = 150, 46
DIAMOND_PAD = 34
GAP_Y, GAP_X = 64, 24
EDGE_RE = re.compile(r"(.+?)\s*->\s*(.+?)(?:\s*\|\s*(.+?)\s*)?$")


def cjk_w(ch):
    return 1.0 if ord(ch) > 0x2E80 else 0.55


def wrap_units(s, fs, max_units):
    lines, cur, cur_w = [], "", 0.0
    for ch in s:
        w = cjk_w(ch)
        if cur_w + w > max_units and cur:
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


def load_style(home):
    colors, fonts = {}, {}
    try:
        with open(os.path.join(home, ".hiq", "skills", "ppt-auto",
                               "template_config.json"), "r", encoding="utf-8") as f:
            cfg = json.load(f)
        colors = cfg.get("colors") or {}
        fonts = cfg.get("fonts") or {}
    except (OSError, ValueError):
        pass
    return {
        "accent": colors.get("accent") or "#4472C4",
        "text": colors.get("text") or "#1A1A1A",
        "muted": colors.get("text_secondary") or "#666666",
        "line": colors.get("line") or "rgba(0,0,0,0.35)",
        "font": fonts.get("family") or '"Microsoft YaHei", sans-serif',
    }


class Node:
    def __init__(self, key, kind):
        self.key, self.kind = key, kind
        self.preds, self.succs = [], []


def parse_dsl(text):
    nodes, edges = {}, []
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].strip()
        if not line or "->" not in line:
            continue
        m = EDGE_RE.match(line)
        if not m:
            continue
        a_raw, b_raw, label = m.group(1).strip(), m.group(2).strip(), (m.group(3) or "").strip()
        def ref(tok):
            t = tok.strip()
            kind = "process"
            if t.startswith("{") and t.endswith("}"):
                kind = "decision"
            elif t.startswith("(") and t.endswith(")"):
                kind = "terminal"
            key = t.strip("[]{}()")
            if key not in nodes:
                nodes[key] = Node(key, kind)
            return nodes[key]
        a, b = ref(a_raw), ref(b_raw)
        if b not in a.succs:
            a.succs.append(b)
            b.preds.append(a)
        edges.append((a, b, label))
    return nodes, edges


def levels_of(nodes):
    """BFS min-depth from sources — cycle-safe (a back edge just points to an
    earlier level; longest-path leveling would inflate levels around loops)."""
    from collections import deque
    lvl = {}
    q = deque()
    for n in nodes.values():
        if not n.preds:
            lvl[n.key] = 0
            q.append(n)
    while q:
        n = q.popleft()
        for s in n.succs:
            if s.key not in lvl or lvl[s.key] > lvl[n.key] + 1:
                lvl[s.key] = lvl[n.key] + 1
                q.append(s)
    for k in nodes:  # cycle members never reached from a source
        lvl.setdefault(k, 0)
    return lvl


def node_box(n, fs):
    lines = wrap_units(n.key, fs, 13.0)
    w = max(NODE_MIN_W, max(sum(cjk_w(c) for c in ln) for ln in lines) * fs + 26)
    if n.kind == "decision":
        w = max(w + DIAMOND_PAD, 120)
        h = NODE_H + DIAMOND_PAD - 10
    else:
        h = max(NODE_H, len(lines) * fs * 1.35 + 18)
    return lines, w, h


def _place(parts, n, lines, x, y, w, h, fs, style, row_h=None):
    """Draw one node at (x, y) with its wrapped label. row_h: vertical-mode row
    height (diamond spans the row); without it the node's own h is used."""
    rh = row_h if row_h is not None else h
    cx, cy = x + w / 2, y + h / 2
    if n.kind == "decision":
        pts = "%.0f,%.0f %.0f,%.0f %.0f,%.0f %.0f,%.0f" % (cx, y, x + w, cy, cx, y + rh, x, cy)
        parts.append('<polygon points="%s" fill="rgba(230,0,18,0.06)" stroke="%s" stroke-width="2"/>' % (pts, style["accent"]))
    elif n.kind == "terminal":
        parts.append('<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="%.0f" fill="%s" opacity="0.12"/>' % (x, y, w, h, h / 2, style["accent"]))
        parts.append('<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="%.0f" fill="none" stroke="%s" stroke-width="2"/>' % (x, y, w, h, h / 2, style["accent"]))
    else:
        parts.append('<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="8" fill="rgba(255,255,255,0.85)" stroke="%s" stroke-width="1.5"/>' % (x, y, w, h, style["line"]))
    ty = cy - (len(lines) - 1) * fs * 0.7 + fs * 0.35
    for ln in lines:
        parts.append('<text x="%.0f" y="%.0f" font-size="%.1f" fill="%s" text-anchor="middle">%s</text>'
                     % (cx, ty, fs, style["text"], esc(ln)))
        ty += fs * 1.35


def render_flow(nodes, edges, fs, style):
    lvl = levels_of(nodes)
    rows = {}
    for k, n in nodes.items():
        rows.setdefault(lvl[k], []).append(n)
    # Deep flows lay out LEFT→RIGHT (levels as columns); shallow ones top-down.
    horizontal = len(rows) > 6
    boxes, parts = {}, []
    y = 96
    max_w_row = 0
    for lv in sorted(rows):
        ns = rows[lv]
        infos = [node_box(n, fs) for n in ns]
        total_w = sum(w for _, w, _ in infos) + GAP_X * (len(ns) - 1)
        max_w_row = max(max_w_row, total_w)
        row_h = max(h for _, _, h in infos)
        if horizontal:
            x = 40 + lv * (max(NODE_MIN_W, 210) + 8)
            col_cy = CANVAS_H / 2 - (sum(h for _, _, h in infos) + GAP_X * (len(ns) - 1)) / 2
            # place nodes vertically within the column, centered
            yy = col_cy
            for n, (lines, w, h) in zip(ns, infos):
                cx, cy = x + w / 2, yy + h / 2
                _place(parts, n, lines, x, yy, w, h, fs, style)
                boxes[n.key] = (x, yy, w, h, cx, cy)
                yy += h + GAP_X
            continue
        x = (CANVAS_W - total_w) / 2
        for n, (lines, w, h) in zip(ns, infos):
            cx, cy = x + w / 2, y + row_h / 2
            boxes[n.key] = (x, y, w, row_h, cx, cy)  # h field = ROW height → ay+ah = row bottom
            _place(parts, n, lines, x, y + (row_h - h) / 2, w, h, fs, style, row_h=row_h)
            x += w + GAP_X
        y += row_h + GAP_Y
    # elbow arrows
    parts.append('<defs><marker id="ah" markerWidth="9" markerHeight="7" refX="8" refY="3.5" orient="auto"><polygon points="0 0, 9 3.5, 0 7" fill="%s"/></marker></defs>' % style["line"])
    for a, b, label in edges:
        ax, ay, aw, ah, acx, acy = boxes[a.key]
        bx, by, bw, bh, bcx, bcy = boxes[b.key]
        fwd = bx > ax  # target sits after the source (right or below)
        if ay == by and fwd:  # same row: straight horizontal connector
            parts.append('<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="%s" stroke-width="1.6" marker-end="url(#ah)"/>' % (ax + aw, acy, bx - 3, bcy, style["line"]))
        elif not fwd:  # same column (horizontal mode) or a back edge
            midy = (ay + ah + by) / 2 if by > ay else ay + ah + 24
            if abs(bcy - midy) < 6 and by > ay:
                parts.append('<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="%s" stroke-width="1.6" marker-end="url(#ah)"/>' % (acx, ay + ah, acx, by, style["line"]))
            else:
                midx = (ax + aw + bx) / 2 if bx > ax + aw else min(ax, bx) - 24
                d = 'M %.0f %.0f L %.0f %.0f L %.0f %.0f L %.0f %.0f L %.0f %.0f' % (
                    acx, ay + ah, acx, midy, midx, midy, midx, bcy, bx if bx > ax else bx + bw, bcy)
                parts.append('<path d="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-dasharray="5,3" marker-end="url(#ah)"/>' % (d, style["line"]))
        else:  # elbow: bottom-center of a's row -> mid-gap -> top of b's row
            y1, y2 = ay + ah, by
            mid = y1 + (y2 - y1) / 2
            d = 'M %.0f %.0f L %.0f %.0f L %.0f %.0f L %.0f %.0f' % (acx, y1, acx, mid, bcx, mid, bcx, y2 - 3)
            parts.append('<path d="%s" fill="none" stroke="%s" stroke-width="1.6" marker-end="url(#ah)"/>' % (d, style["line"]))
            if label:
                lx = (acx + bcx) / 2
                parts.append('<rect x="%.0f" y="%.0f" width="%d" height="16" fill="#FFFFFF" opacity="0.9"/>' % (lx - len(label) * 6, mid - 8, len(label) * 12 + 6))
                parts.append('<text x="%.0f" y="%.0f" font-size="11" fill="%s" text-anchor="middle">%s</text>' % (lx + 3, mid + 4, style["accent"], esc(label)))
    return parts, y


def render_timeline(chain, fs, style):
    """chain: list of (date, task). Horizontal snake: 1 row ≤ 9 nodes else 2."""
    parts = ['<line x1="80" y1="240" x2="1200" y2="240" stroke="%s" stroke-width="2"/>' % style["accent"]]
    n = len(chain)
    rows = 1 if n <= 9 else 2
    per = (n + rows - 1) // rows
    for i, (date, task) in enumerate(chain):
        r, c = divmod(i, per)
        x = 100 + c * (1040 / max(1, per - 1)) if per > 1 else 640
        y = 240 if r == 0 or rows == 1 else 470
        parts.append('<circle cx="%.0f" cy="%d" r="7" fill="%s"/>' % (x, y, style["accent"]))
        above = (r == 0 and rows == 2) or (rows == 1 and i % 2 == 0)
        lines = wrap_units(task, 11.5, 16.0)
        ty = y - 22 - len(lines) * 14 if above else y + 34
        parts.append('<text x="%.0f" y="%d" font-size="13" font-weight="bold" fill="%s" text-anchor="middle">%s</text>'
                     % (x, ty - 16 if above else ty, style["text"], esc(date)))
        for ln in lines:
            parts.append('<text x="%.0f" y="%d" font-size="11.5" fill="%s" text-anchor="middle">%s</text>'
                         % (x, ty, style["muted"], esc(ln)))
            ty += 15
    return parts


def main():
    ap = argparse.ArgumentParser(description="Mechanical flowchart / timeline slide generator.")
    ap.add_argument("dsl", nargs="?", help="flow DSL file")
    ap.add_argument("--timeline", action="store_true", help="timeline mode")
    ap.add_argument("--from-table", dest="from_table",
                    help="2-col markdown table (date | task) for --timeline")
    ap.add_argument("--out", required=True)
    ap.add_argument("--title", default="")
    ap.add_argument("--home", default=None)
    args = ap.parse_args()

    home = args.home or os.path.expanduser("~")
    style = load_style(home)
    parts = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="%s">'
             % (CANVAS_W, CANVAS_H, esc(style["font"]))]
    if args.title:
        parts.append('<text x="60" y="46" font-size="26" font-weight="bold" fill="%s">%s</text>'
                     % (style["text"], esc(args.title)))
        parts.append('<rect x="60" y="58" width="60" height="3" fill="%s"/>' % style["accent"])

    summary = {}
    if args.timeline:
        if not args.from_table or not os.path.isfile(args.from_table):
            print(json.dumps({"error": "--timeline needs --from-table <md>"}))
            return 1
        chain = []
        for line in open(args.from_table, encoding="utf-8"):
            t = line.strip()
            if not t.startswith("|") or set(t.replace("|", "").strip()) <= {"-", ":", " "}:
                continue
            cells = [c.strip() for c in t.strip("|").split("|")]
            if len(cells) >= 2 and cells[0] and cells[0] != "时间点":
                chain.append((cells[0], cells[1] if len(cells) > 1 else ""))
        parts += render_timeline(chain, 11.5, style)
        summary = {"timeline_nodes": len(chain)}
    else:
        if not args.dsl or not os.path.isfile(args.dsl):
            print(json.dumps({"error": "need a DSL file (see header comment)"}))
            return 1
        nodes, edges = parse_dsl(open(args.dsl, encoding="utf-8").read())
        if not nodes:
            print(json.dumps({"error": "no edges parsed from DSL"}))
            return 1
        fs = 13.5
        flow, bottom = render_flow(nodes, edges, fs, style)
        parts += flow
        summary = {"nodes": len(nodes), "edges": len(edges), "bottom_y": round(bottom)}

    parts.append("</svg>")
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write("\n".join(parts))
    summary["out"] = args.out
    print(json.dumps(summary, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
