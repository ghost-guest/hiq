#!/usr/bin/env python3
"""One-shot preflight for the SVG route: template colors + visual merge +
project init + consolidated config summary.

Replaces what used to be five separate agent round trips (ls template →
extract_template_colors → merge_vlm_style → read template_config.json →
project_manager init) with ONE bash call. The printed JSON is everything the
model needs from Step 0 / Step 3 / Step 4: final colors, fonts, mode, whether
references exist, and the created project_dir.

Usage:
    python3 preflight.py <project_name>     # init included
    python3 preflight.py                    # colors/merge only, no project

Exit code is 0 unless project init fails; sub-step statuses live in the JSON
(steps[].rc) so the model can decide instead of parsing prose.
"""
import json
import re
import subprocess
import sys
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
SKILL_DIR = SCRIPTS.parent
HIQ_DIR = Path.home() / ".hiq"


def run(cmd):
    p = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8", errors="replace")
    return p.returncode, ((p.stdout or "") + (p.stderr or "")).strip()


def main():
    project_name = sys.argv[1].strip() if len(sys.argv) > 1 else ""
    py = sys.executable or "python3"
    cfg_path = SKILL_DIR / "template_config.json"

    summary = {
        "has_template": False,
        "reference_style": False,
        "pdf_pages": 0,
        "project_dir": "",
        "steps": [],
    }

    tpl = HIQ_DIR / "ppt-template.pptx"
    summary["has_template"] = tpl.exists()
    if tpl.exists():
        rc, out = run([py, str(SCRIPTS / "extract_template_colors.py"), str(tpl), str(cfg_path)])
        summary["steps"].append({"step": "extract_template_colors", "rc": rc, "out": out[-400:]})
    # Merge runs whenever ANY VLM style file exists — a reference image's
    # colors must reach the config even when the user picked no template
    # (S-21: the merge used to be gated on the template and silently skipped).
    has_style = any((HIQ_DIR / name).exists()
                    for name in ("ppt-template-style.json", "reference-style.json"))
    if tpl.exists() or has_style:
        rc, out = run([py, str(SCRIPTS / "merge_vlm_style.py"), str(cfg_path)])
        summary["steps"].append({"step": "merge_vlm_style", "rc": rc, "out": out[-400:]})

    summary["reference_style"] = (HIQ_DIR / "reference-style.json").exists()
    pages_dir = HIQ_DIR / "pdf-pages"
    if pages_dir.is_dir():
        summary["pdf_pages"] = len(list(pages_dir.glob("page-*.json")))

    if project_name:
        rc, out = run([py, str(SCRIPTS / "project_manager.py"), "init", project_name, "--format", "ppt169"])
        summary["steps"].append({"step": "project_init", "rc": rc, "out": out[-400:]})
        m = re.search(r"Project created:\s*(.+)", out)
        if m:
            summary["project_dir"] = m.group(1).strip()
        if rc != 0:
            print(json.dumps(summary, ensure_ascii=False, indent=2))
            sys.exit(1)

    try:
        cfg = json.loads(cfg_path.read_text(encoding="utf-8"))
        summary["colors"] = cfg.get("colors", {})
        summary["fonts"] = cfg.get("fonts", {})
        summary["mode"] = cfg.get("mode", "fast")
    except Exception as exc:  # unreadable/missing config — report, don't crash
        summary["config_error"] = str(exc)

    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
