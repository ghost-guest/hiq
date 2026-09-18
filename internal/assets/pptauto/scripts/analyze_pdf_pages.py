#!/usr/bin/env python3
"""Complete the per-page visual analysis of a reference PDF — ALL pages.

Usage:
    python analyze_pdf_pages.py <pdf_path> [--home <dir>] [--pool 3] [--scale 2]

WHY this exists: the desktop pre-analysis (PreparePPTReference) renders and
VLM-analyzes only the FIRST 6 pages — a deliberate submit-path latency cap. For
a 32-page deck that left 26 pages without a visual reference, and the main model
filled the gap by extracting raw TEXT (fitz get_text), which flattens tables
into word soup. This script runs INSIDE the ppt-auto skill (bash, off the
submit path) and completes the missing pages: renders every page to PNG,
VLM-describes the ones lacking a page-N.json, idempotently reusing whatever the
desktop already analyzed.

Writes ~/.hiq/pdf-pages/page-N.json in the exact shape the desktop path
produces (page/image/total_pages/verdict/description/error), so ppt-auto Step 3
consumes both sources uniformly. Never touches reference-style.json (deck
colors are the desktop's job; clobbering them here would break idempotency).

VLM access reuses qa_compare's config layering (user config.toml + project
hiq.toml merged like the Go loader). Any failure degrades per page — one
bad page records its error and the rest proceed. Exits 0 with a JSON summary
(complete_step needs successful bash receipts); usage errors exit 1.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
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

try:
    import qa_compare  # same dir — reuse config layering + data URL helpers
except ImportError:
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import qa_compare

import pymupdf  # PyMuPDF — renders the pages (skill requirement, fitz alias)

VLM_TIMEOUT_SECONDS = 90

# The merged analyzer prompt — SAME contract as classify_reference.go's
# pptRefAnalyzerPrompt (verdict on the first line, PLAIN → transcription,
# VISUAL → 4 sections). Keep the two in sync when editing either side.
ANALYZER_PROMPT = """You are looking at ONE page (a reference for recreating it as an editable PowerPoint slide).

FIRST LINE — the verdict, exactly one word:
PLAIN — only words and numbers, NO visual styling, layout, or design (a plain text note, a body-text document page, a code listing, a raw data dump).
VISUAL — has visual design: styled/large titles, a color scheme, multi-column layout, charts/diagrams, cards/boxes, icons, decorative shapes, or a slide-like layout.

If PLAIN: after the first line, transcribe ALL visible text verbatim (preserve line breaks; top-to-bottom, left-to-right reading order). Output ONLY the transcription — no headings, no commentary, no markdown formatting. If the image contains no text at all, output exactly: (no text)

If VISUAL: after the first line, describe the page in exactly 4 markdown sections:
## 1 CONTENT
Transcribe ALL text verbatim — title, headings, body paragraphs, every bullet point, captions, data labels, table cells. Do NOT summarize. For TABLES give every row and column with its exact cell text. Mark text you cannot read clearly as (illegible) — never guess.
## 2 LAYOUT
The spatial arrangement: where the title sits, body region, single vs multi-column, paragraphs vs bullet list, any table (give its rows and columns count and the header row), image regions (position and rough size share).
End this section with one fenced json block of rough region boxes — pixel estimates on a 1280x720 canvas, 4-8 regions, rough is fine:
```json
[{"type":"text|card|image|table|chart","bbox":[x,y,w,h],"content":"key text or short label"}]
```
## 3 FORMAT
Relative font sizing, weight cues, bullet style, text alignment per block.
## 4 DESIGN
Background (light/dark, hue name), accent colors (hue names), overall style, any logo or decorative elements.

Text (CONTENT) must be exact — use (illegible) for unreadable text instead of guessing. Positions and colors can be approximate. Output ONLY the verdict line followed by the required content, no preamble, no closing remarks."""


def parse_analysis(resp):
    """Mirror of classify_reference.go's parseRefAnalysis (tolerant first-line
    verdict; ambiguous → PLAIN, the cheap path). Keep in sync with the Go side."""
    lines = resp.split("\n")
    for i, raw in enumerate(lines):
        clean = raw.strip().lstrip("`").lstrip("#").strip()
        if not clean:
            continue
        words = clean.upper().split()
        w = words[0].rstrip(":.").strip("`\"'") if words else ""
        if w in ("VERDICT", "ANSWER") and len(words) > 1:
            w = words[1].rstrip(":.").strip("`\"'")
        if w in ("VISUAL", "B"):
            verdict = "VISUAL"
        elif w in ("PLAIN", "A"):
            verdict = "PLAIN"
        else:
            return "PLAIN?", resp.strip()
        return verdict, "\n".join(lines[i + 1:]).strip()
    return "PLAIN?", resp.strip()


def render_pages(pdf_path, out_dir, scale):
    doc = pymupdf.open(pdf_path)
    total = len(doc)
    paths = []
    for i, page in enumerate(doc, start=1):
        png = os.path.join(out_dir, "page-%d.png" % i)
        if not os.path.isfile(png):  # reuse the desktop's existing renders
            pix = page.get_pixmap(matrix=pymupdf.Matrix(scale, scale))
            pix.save(png)
        paths.append((i, png))
    doc.close()
    return paths, total


def main():
    ap = argparse.ArgumentParser(description="Complete per-page VLM analysis of a reference PDF.")
    ap.add_argument("pdf_path")
    ap.add_argument("--home", default=None, help="home dir containing .hiq/ (default: ~)")
    ap.add_argument("--pool", type=int, default=3, help="concurrent VLM calls (providers rate-limit)")
    ap.add_argument("--scale", type=int, default=2, help="page render scale")
    ap.add_argument("--max", type=int, default=8, dest="max_batch",
                    help="analyze at most N missing pages per run (keeps each "
                         "invocation under the skill bash 2-minute timeout); "
                         "rerun until remaining=0")
    args = ap.parse_args()

    home = args.home or os.path.expanduser("~")
    out_dir = os.path.join(home, ".hiq", "pdf-pages")

    def summary(total, analyzed, skipped, failed, note=""):
        obj = {"total": total, "analyzed": analyzed, "skipped_existing": skipped,
               "failed": failed, "out_dir": out_dir, "note": note}
        print(json.dumps(obj, ensure_ascii=False))

    pdf_path = args.pdf_path
    if not os.path.isfile(pdf_path):
        # The task prompt may relay a wrong path (e.g. ~/.hiq/attachments
        # instead of the workspace copy). reference-style.json's source_path is
        # the canonical absolute path the desktop analyzed — fall back to it
        # instead of letting the agent go searching the whole disk.
        try:
            with open(os.path.join(home, ".hiq", "reference-style.json"), "r", encoding="utf-8") as f:
                src = str(json.load(f).get("source_path") or "")
            if src and os.path.isfile(src):
                pdf_path = src
        except (OSError, ValueError):
            pass
    if not os.path.isfile(pdf_path):
        summary(0, 0, 0, 0, "PDF not found: %s (reference-style.json source_path fallback also missed)" % pdf_path)
        return 0
    try:
        os.makedirs(out_dir, exist_ok=True)
        pages, total = render_pages(pdf_path, out_dir, args.scale)
        # Drop a previous longer PDF's leftovers (page-K with K > this total):
        # stale analyses must never be consumed as pages of the current one.
        import glob as _glob
        import re as _re
        for f in _glob.glob(os.path.join(out_dir, "page-*")):
            m = _re.search(r"page-(\d+)", os.path.basename(f))
            if m and int(m.group(1)) > total:
                try:
                    os.remove(f)
                except OSError:
                    pass
    except Exception as e:  # noqa: BLE001 — render failure degrades the whole run
        summary(0, 0, 0, 0, "render failed: %s" % e)
        return 0

    # VLM access: any failure here is a skip, never a blocker.
    cfg, config_dir = qa_compare.load_hiq_config()
    vlm, why_not = (None, "vlm_config_missing")
    if cfg is not None:
        vlm, why_not = qa_compare.resolve_vlm(cfg, config_dir)
    if vlm is None:
        summary(total, 0, 0, 0, "VLM unavailable: %s" % why_not)
        return 0

    def write_page(n, png, result):
        with open(os.path.join(out_dir, "page-%d.json" % n), "w", encoding="utf-8") as f:
            json.dump(result, f, ensure_ascii=False, indent=2)

    def analyze(n_png):
        n, png = n_png
        result = {"page": n, "image": os.path.basename(png), "total_pages": total}
        try:
            resp = qa_compare.vlm_call(vlm, ANALYZER_PROMPT, [qa_compare.data_url(png)])
            verdict, body = parse_analysis(resp)
            result["verdict"] = verdict
            result["description"] = body
        except Exception as e:  # noqa: BLE001 — per-page isolation
            result["error"] = str(e)
        write_page(n, png, result)
        return result

    todo, skipped = [], 0
    remaining_after = 0
    for n, png in pages:
        jpath = os.path.join(out_dir, "page-%d.json" % n)
        try:
            with open(jpath, "r", encoding="utf-8") as f:
                if json.load(f).get("description"):
                    skipped += 1  # desktop already analyzed this page — reuse
                    continue
        except (OSError, ValueError):
            pass
        todo.append((n, png))

    # Batch cap: the skill's bash tool kills commands at ~2 minutes. Analyzing
    # all missing pages in one go WILL be killed mid-run on big PDFs; capping
    # per-run work + idempotent reruns makes progress monotonic.
    batch = todo[:max(0, args.max_batch)]
    remaining_after = len(todo) - len(batch)
    todo = batch

    failed = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.pool)) as pool:
        for r in pool.map(analyze, todo):
            failed += 1 if r.get("error") else 0
    summary(total, len(todo) - failed, skipped, failed,
            "remaining_unanalyzed=%d (rerun until 0)" % remaining_after if remaining_after else "")
    return 0


if __name__ == "__main__":
    sys.exit(main())
