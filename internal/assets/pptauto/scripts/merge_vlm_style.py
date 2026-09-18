#!/usr/bin/env python3
"""Merge VLM-extracted style into template_config.json (the missing mechanical step).

PROBLEM this fixes:
  ppt-template-style.json (VLM colors, produced when the user picks a template via
  PickPPTTemplate → analyzeTemplateStyleAsync) was NEVER code-merged into
  template_config.json. The skill (SKILL.md Step 3) just told the LLM "read both
  files, the VLM one wins on color" — relying on the LLM's discipline. grep across
  the whole repo confirmed no merge existed. So "VLM overrides config colors" was
  not actually true in code.

WHAT this does:
  Reads ~/.hiq/ppt-template-style.json (and, when Phase 2 produces it,
  ~/.hiq/reference-style.json) and merges their colors into the given
  template_config.json IN PLACE. After this runs, the config that ppt-auto and
  check_svg.py read already reflects VLM-extracted colors — no LLM discipline needed.

PRIORITY (highest wins, applied last):
  1. reference-style.json   (Phase 2: reference-image colors, if it carries hex fields)
  2. ppt-template-style.json (VLM template colors, from PickPPTTemplate)
  3. template_config.json    (baseline / extract_template_colors.py output from Step 0)

SCOPE (deliberately narrow, per PPT vision spec):
  Only `colors` are merged (background/accent/text + is_dark-derived secondary/
  muted/card_bg/line). Fonts, canvas (16:9), font sizes, layout are NOT touched —
  background stays default.pptx, canvas stays 1280x720, font sizes autofit elsewhere.
  Run AFTER extract_template_colors.py (Step 0) so VLM colors win over PIL extraction.

Usage:
    python merge_vlm_style.py <template_config.json> [--home <home_dir>]
    # reads ~/.hiq/{ppt-template-style,reference-style}.json (whichever exist),
    # merges into <template_config.json> in place.
    # Last stdout line: {"merged": [<files>], "config": <path>}
"""
import argparse
import json
import os
import re
import sys


def _is_hex(value):
    """Strict #RRGGBB check — anything else (hue names, rgb(), short hex) is
    ignored so an off-format VLM reply can't pollute the config with values
    that would end up verbatim in SVG fill attributes (S-11)."""
    return isinstance(value, str) and re.match(r'^#[0-9a-fA-F]{6}$', value) is not None


def _dark_palette(is_dark):
    """is_dark-derived secondary colors.

    Mirrors extract_template_colors.py:329-346 so a VLM is_dark flag produces the
    SAME derived palette the PIL path would — text/secondary/card_bg/line all stay
    consistent with the VLM-detected background brightness.
    """
    if is_dark:
        return {
            "primary": "#FFFFFF",
            "text": "#FFFFFF",
            "secondary": "#A0A0A0",
            "secondary_light": "#2A2A2A",
            "text_muted": "#888888",
            "text_secondary": "#A0A0A0",
            "card_bg": "rgba(255,255,255,0.08)",
            "line": "rgba(255,255,255,0.15)",
        }
    return {
        "primary": "#1A1A1A",
        "text": "#1A1A1A",
        "secondary": "#666666",
        "secondary_light": "#E8E8E8",
        "text_muted": "#999999",
        "text_secondary": "#666666",
        "card_bg": "rgba(255,255,255,0.75)",
        "line": "rgba(0,0,0,0.1)",
    }


def _apply_vlm_style(config, vlm_style, allow_background_type=True):
    """Merge one VLM style dict (ppt-template-style.json shape) into config['colors'].

    Maps the VLM result fields (background / is_dark / accent_colors / text_color /
    background_type) onto config's color keys, deriving the secondary/muted/card_bg/
    line palette from is_dark. Tolerates missing fields (Phase 2's reference-style.json
    may carry only a subset).

    allow_background_type: only ppt-template-style.json (a REAL .pptx template)
    may set background_type — it flips the whole pipeline into "template mode"
    (no background in SVG, master shows through). reference-style.json's
    background_type describes the reference PAGE's look, not a template's
    existence; copying it made template-less decks lose their background.
    """
    colors = config.setdefault("colors", {})

    bg = vlm_style.get("background")
    if bg:
        if _is_hex(bg):
            colors["background"] = bg
        else:
            print(f"[merge_vlm_style] WARN non-hex background ignored: {bg!r}", file=sys.stderr)
    bt = vlm_style.get("background_type")
    if bt and allow_background_type:
        colors["background_type"] = bt

    # is_dark-derived palette — overrides baseline so text/secondary match the VLM bg.
    if "is_dark" in vlm_style:
        colors.update(_dark_palette(bool(vlm_style.get("is_dark"))))

    # VLM text_color (if given) overrides the is_dark default for text/primary.
    tc = vlm_style.get("text_color")
    if tc:
        if _is_hex(tc):
            colors["text"] = tc
            colors["primary"] = tc
        else:
            print(f"[merge_vlm_style] WARN non-hex text_color ignored: {tc!r}", file=sys.stderr)

    # accent: first valid hex accent wins; leave existing if none.
    raw_accents = vlm_style.get("accent_colors") or []
    accents = [a for a in raw_accents if _is_hex(a)]
    if len(accents) < len(raw_accents):
        print(f"[merge_vlm_style] WARN non-hex accent_colors ignored: "
              f"{raw_accents!r}", file=sys.stderr)
    if accents:
        colors["accent"] = accents[0]

    colors["white"] = "#FFFFFF"


def merge(config_path, home_dir):
    with open(config_path, "r", encoding="utf-8") as f:
        config = json.load(f)

    hiq = os.path.join(home_dir, ".hiq")
    # Apply LOWEST priority first so higher priority overwrites.
    # (reference-style wins over template-style wins over baseline.)
    # allow_background_type only for the template source — see
    # _apply_vlm_style for why a reference's background_type must not
    # flip a template-less deck into template mode.
    sources = [
        ("ppt-template-style.json", os.path.join(hiq, "ppt-template-style.json"), True),
        ("reference-style.json", os.path.join(hiq, "reference-style.json"), False),
    ]
    applied = []
    for name, path, allow_bt in sources:
        if not os.path.isfile(path):
            continue
        try:
            with open(path, "r", encoding="utf-8") as sf:
                style = json.load(sf)
            _apply_vlm_style(config, style, allow_background_type=allow_bt)
            applied.append(name)
        except (OSError, ValueError) as e:
            print(f"[merge_vlm_style] WARN skipping {name}: {e}", file=sys.stderr)

    if not applied:
        print("[merge_vlm_style] no VLM style files found; config unchanged", file=sys.stderr)
        print(json.dumps({"merged": [], "config": config_path}))
        return 0

    with open(config_path, "w", encoding="utf-8") as f:
        json.dump(config, f, ensure_ascii=False, indent=2)
    print(json.dumps({"merged": applied, "config": config_path}))
    return 0


def main():
    ap = argparse.ArgumentParser(description="Merge VLM style into template_config.json.")
    ap.add_argument("config_path", help="Path to template_config.json (merged in place)")
    ap.add_argument("--home", default=None, help="Home dir containing .hiq/ (default: ~)")
    args = ap.parse_args()

    if not os.path.isfile(args.config_path):
        print(json.dumps({"error": f"config not found: {args.config_path}"}))
        sys.exit(1)

    home = args.home or os.path.expanduser("~")
    sys.exit(merge(args.config_path, home))


if __name__ == "__main__":
    main()
