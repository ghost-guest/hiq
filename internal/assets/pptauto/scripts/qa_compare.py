#!/usr/bin/env python3
"""Visual QA loop: compare generated slide SVGs against the reference image/PDF.

Usage:
    python qa_compare.py <project_dir> [--round N] [--home <home_dir>]

Runs AFTER Step 6 (SVG generation) and BEFORE Step 7 (PPTX conversion), only
when the deck was generated from a reference:

  - PDF reference:  ~/.hiq/pdf-pages/page-N.png  ↔ svg_output/slide_NN.svg
  - single image:   ~/.hiq/reference-style.json's source_path ↔ slide_01.svg

Each page pair is rendered to PNG (cairosvg) and sent to the configured VLM
(the same cowork.vlm_model the desktop pre-analysis uses, read from hiq's
config) with a severity-gated compare prompt. Output is a JSON report:

    {"round": 1, "stop": false, "pages": [{"page": 1, "verdict": "MAJOR", ...}]}

Loop control is MECHANICAL and lives in this script, not in the LLM's judgment:
  - hard cap: --round > 2 → stop (max_rounds); after round 2 accept the result.
  - no-progress: identical (page, verdict, issues) as the previous round's
    qa-report.json → stop (no_progress); reworking again would just churn.
  - severity gate: only MAJOR verdicts ask for a rework; MINOR is reported but
    never blocks. The goal is "similar", not pixel-identical — nitpicks churn.

The script ALWAYS exits 0 with a report (except bare usage errors): the skill's
complete_step evidence must match a successful bash receipt, and the SKILL.md
instructions tell the agent to obey the "stop" flag rather than the exit code.

VLM access is reconstructed from hiq's own config (project ./hiq.toml
taking precedence over the user config dir), resolving the model ref
("<provider>/<model>") to its provider entry, and the API key from the
provider's api_key_env (environment first, then the credentials file beside
config.toml). Any failure here degrades to a stop-with-reason report — QA is an
optional fidelity pass, never a blocker.
"""

from __future__ import annotations

import argparse
import base64
import concurrent.futures
import glob
import json
import os
import re
import sys
import urllib.error
import urllib.request

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

MAX_ROUNDS = 2          # hard cap: rework at most twice, then accept the result
VLM_TIMEOUT_SECONDS = 60
COMPARE_POOL = 3        # concurrent page comparisons — providers rate-limit


# ── config / VLM access ─────────────────────────────────────────────────────

def _toml_load(path):
    try:
        import tomllib  # Python 3.11+
        with open(path, "rb") as f:
            return tomllib.load(f)
    except ImportError:
        try:
            import tomli
            with open(path, "rb") as f:
                return tomli.load(f)
        except ImportError:
            return None
    except OSError:
        return None


def _user_config_dir():
    if sys.platform == "win32":
        return os.environ.get("APPDATA", "")
    if sys.platform == "darwin":
        return os.path.join(os.path.expanduser("~"), "Library", "Application Support")
    return os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")


def _deep_merge(base, overlay):
    """Dicts merge recursively; scalars/lists are replaced (later wins) — the
    same per-field semantics as the Go loader's mergeFile chain."""
    for k, v in overlay.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            _deep_merge(base[k], v)
        else:
            base[k] = v
    return base


def load_hiq_config():
    """Match the Go loader's layering: user config.toml as the base, the
    project ./hiq.toml merged on top (per-field, later wins). Returning
    only the FIRST existing file was a bug: a project hiq.toml without
    provider/vlm keys shadowed the user config entirely, so qa_compare
    reported vlm_not_configured while the desktop's correctly-layered
    pre-analysis worked fine on the same machine."""
    base = None
    config_dir = ""
    uc = os.path.join(_user_config_dir(), "hiq", "config.toml")
    if os.path.isfile(uc):
        base = _toml_load(uc)
        if base is not None:
            config_dir = os.path.dirname(uc)
    proj = os.path.join(os.getcwd(), "hiq.toml")
    if os.path.isfile(proj):
        p = _toml_load(proj)
        if p is not None:
            base = _deep_merge(base, p) if base is not None else p
            if config_dir == "":
                config_dir = os.path.dirname(proj)
    return base, config_dir


def _credentials_key(config_dir, env_name):
    """Resolve the provider key: environment first, then KEY=value lines in the
    credentials file beside config.toml (the same file hiq loads)."""
    val = os.environ.get(env_name, "").strip()
    if val:
        return val
    if not config_dir:
        return ""
    cred_path = os.path.join(config_dir, "credentials")
    try:
        with open(cred_path, "r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                if k.strip() == env_name:
                    return v.strip().strip('"').strip("'")
    except OSError:
        pass
    return ""


def resolve_vlm(cfg, config_dir):
    """cowork.vlm_model (fallback screenshot_vlm_model) → (endpoint, model, key).
    Only OpenAI-compatible providers are supported here; others degrade."""
    cowork = cfg.get("cowork", {}) or {}
    ref = str(cowork.get("vlm_model") or cowork.get("screenshot_vlm_model") or "").strip()
    if not ref:
        return None, "vlm_not_configured"
    providers = cfg.get("providers", []) or []
    entry = None
    model = ""
    if "/" in ref:
        prov_name, model = ref.split("/", 1)
        for p in providers:
            if str(p.get("name", "")) == prov_name:
                entry = p
                break
    if entry is None:
        # a bare provider name → its default model; a bare model name → the provider listing it
        for p in providers:
            if str(p.get("name", "")) == ref:
                entry = p
                model = str(p.get("default") or (p.get("models") or [""])[0])
                break
    if entry is None:
        for p in providers:
            if ref in (p.get("models") or []):
                entry, model = p, ref
                break
    if entry is None:
        return None, "vlm_provider_not_found"
    kind = str(entry.get("kind", "") or "openai").lower()
    if kind not in ("openai", "openai-compatible", ""):
        return None, "vlm_unsupported_kind"
    base_url = str(entry.get("base_url", "")).rstrip("/")
    if not base_url:
        return None, "vlm_no_base_url"
    key = _credentials_key(config_dir, str(entry.get("api_key_env", "")))
    if not key:
        return None, "vlm_no_api_key"
    model = model or str(entry.get("default") or (entry.get("models") or [""])[0])
    if not model:
        return None, "vlm_no_model"
    return {
        "endpoint": base_url + "/chat/completions",
        "model": model,
        "key": key,
    }, None


# ── images ───────────────────────────────────────────────────────────────────

def _mime_of(data):
    if data[:8] == b"\x89PNG\r\n\x1a\n":
        return "image/png"
    if data[:3] == b"\xff\xd8\xff":
        return "image/jpeg"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    return "image/png"


def data_url(path):
    with open(path, "rb") as f:
        data = f.read()
    return "data:" + _mime_of(data) + ";base64," + base64.b64encode(data).decode("ascii")


def _deck_background(home):
    """The deck's background color (D-04 fallback chain):
    template_config.json's colors.background (what merge_vlm_style last wrote)
    → reference-style.json's TOP-LEVEL background (flat field, not colors.*)
    → #FFFFFF. Template-mode SVGs deliberately draw NO background — rendered
    standalone their transparent canvas shows as BLACK to the VLM, which QA
    then misreads as an inverted background. Compositing onto the deck
    background fixes it."""
    bg = ""
    try:
        with open(os.path.join(home, ".hiq", "skills", "ppt-auto",
                               "template_config.json"), "r", encoding="utf-8") as f:
            bg = str((json.load(f).get("colors") or {}).get("background") or "")
    except (OSError, ValueError):
        pass
    if not (bg.startswith("#") and len(bg) == 7):
        try:
            with open(os.path.join(home, ".hiq", "reference-style.json"),
                      "r", encoding="utf-8") as f:
                bg = str(json.load(f).get("background") or "")
        except (OSError, ValueError):
            bg = ""
    return bg if bg.startswith("#") and len(bg) == 7 else "#FFFFFF"


def _flatten_alpha(png_path, bg_hex):
    """Composite the rendered SVG's transparent pixels onto a solid background
    (best-effort — without Pillow the false-black risk stays, as MINOR noise)."""
    try:
        from PIL import Image
        im = Image.open(png_path)
        if im.mode == "RGBA":
            rgb = tuple(int(bg_hex[i:i + 2], 16) for i in (1, 3, 5))
            base = Image.new("RGB", im.size, rgb)
            base.paste(im, mask=im.split()[-1])
            base.save(png_path)
    except ImportError:
        pass


RENDERER = os.environ.get("PPTQA_RENDERER", "auto").lower()  # auto|resvg|playwright


def _render_playwright(svg_path, png_path):
    """Chromium rendering — the most robust CJK font-fallback chain (ppt-master
    rejected cairo for exactly this reason). Requires playwright + chromium;
    ImportError propagates to the caller which falls back to resvg."""
    from playwright.sync_api import sync_playwright
    with open(svg_path, "r", encoding="utf-8") as f:
        svg = f.read()
    with sync_playwright() as pw:
        page = pw.chromium.launch().new_page(viewport={"width": 1280, "height": 720})
        page.set_content(svg, wait_until="load")
        page.screenshot(path=png_path, full_page=False)


def render_svg(svg_path, png_path):
    """Render one slide SVG to PNG. Backend order: PPTQA_RENDERER env selects
    playwright explicitly; default chain is cairosvg → resvg-py → playwright.
    cairosvg needs the native cairo DLL (stock Windows Pythons don't ship it —
    it raises OSError at import), resvg-py is the zero-dependency Rust fallback
    with working CJK on this machine; playwright is the font-chain-safest last
    resort when installed. Any failure propagates and the page degrades."""
    if RENDERER == "playwright":
        _render_playwright(svg_path, png_path)
        return
    try:
        import cairosvg
        cairosvg.svg2png(url=svg_path, write_to=png_path, output_width=1280)
        return
    except (ImportError, OSError):
        pass
    try:
        import resvg_py
        with open(svg_path, "r", encoding="utf-8") as f:
            svg = f.read()
        with open(png_path, "wb") as f:
            f.write(resvg_py.svg_to_bytes(svg_string=svg, width=1280))
        return
    except ImportError:
        pass
    _render_playwright(svg_path, png_path)


# ── the compare prompt (severity-gated: only MAJOR asks for a rework) ───────

COMPARE_PROMPT = """Image 1 is the REFERENCE; image 2 is a GENERATED slide meant to look SIMILAR to it (not pixel-identical). Judge fidelity:
- MAJOR: a content block/section clearly present in the reference is missing; the structure is clearly different (e.g. reference has a 2x2 card grid, generated has a plain list); the accent color scheme is clearly wrong (reference blue-themed, generated red-themed); text is truncated or overlapping so content is unreadable.
- MINOR: small spacing/size differences, slight alignment offsets, minor wording differences.
- PASS: no MAJOR issue. Minor differences are ACCEPTABLE — the goal is "similar", not identical.
Respond with ONLY a JSON object: {"verdict":"PASS"|"MINOR"|"MAJOR","issues":["short description", ...]} — list only real problems; empty list for PASS."""


def vlm_call(vlm, prompt, image_urls, max_tokens=4096):
    """One VLM call: a text prompt plus N images (data URLs) -> text content.
    Shared by the QA compare below and analyze_pdf_pages' per-page analyzer."""
    content = [{"type": "text", "text": prompt}]
    for u in image_urls:
        content.append({"type": "image_url", "image_url": {"url": u}})
    payload = {
        "model": vlm["model"],
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": content}],
    }
    req = urllib.request.Request(
        vlm["endpoint"],
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + vlm["key"],
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=VLM_TIMEOUT_SECONDS) as resp:
        body = json.loads(resp.read().decode("utf-8"))
    return body["choices"][0]["message"]["content"] or ""


RUBRIC_PROMPT = """You are reviewing ONE generated presentation slide against ABSOLUTE quality standards (no reference image exists). Judge:
- MAJOR: text overflows its container or is visibly truncated; text blocks overlap each other making content unreadable; text color contrasts too weakly with its background (dark-on-dark / light-on-light); layout is chaotically misaligned (elements at random positions); more than 40% of the content area is empty on a content page; no font-size hierarchy at all (everything the same size, no title distinction).
- MINOR: small spacing/alignment imperfections; slightly uneven margins; minor color imbalance.
- PASS: none of the MAJOR issues. Covers/section/ending pages are ALLOWED to be sparse — do not judge generous whitespace on them.
Respond with ONLY a JSON object: {"verdict":"PASS"|"MINOR"|"MAJOR","issues":["short description", ...]} — empty list for PASS. Do not nitpick subjective taste."""


def vlm_rubric(vlm, gen_url):
    """Absolute-standard review of one generated slide (no reference)."""
    try:
        text = vlm_call(vlm, RUBRIC_PROMPT, [gen_url], max_tokens=1024)
    except Exception:  # noqa: BLE001
        return {"verdict": "PASS", "issues": []}
    m = re.search(r"\{.*\}", text, re.S)
    if m:
        try:
            obj = json.loads(m.group(0))
            verdict = str(obj.get("verdict", "PASS")).upper()
            if verdict not in ("PASS", "MINOR", "MAJOR"):
                verdict = "PASS"
            return {"verdict": verdict, "issues": [str(i) for i in (obj.get("issues") or [])][:5]}
        except ValueError:
            pass
    return {"verdict": "PASS", "issues": []}


def vlm_compare(vlm, ref_url, gen_url):
    try:
        text = vlm_call(vlm, COMPARE_PROMPT, [ref_url, gen_url], max_tokens=1024)
    except Exception:  # noqa: BLE001 — network/HTTP failure must not block the page
        return {"verdict": "PASS", "issues": []}
    # Pull the JSON object out of a possibly chatty response.
    m = re.search(r"\{.*\}", text, re.S)
    if m:
        try:
            obj = json.loads(m.group(0))
            verdict = str(obj.get("verdict", "PASS")).upper()
            if verdict not in ("PASS", "MINOR", "MAJOR"):
                verdict = "PASS"
            issues = [str(i) for i in (obj.get("issues") or [])][:5]
            return {"verdict": verdict, "issues": issues}
        except ValueError:
            pass
    return {"verdict": "PASS", "issues": []}  # unparseable -> don't block the deck


# ── reference resolution ────────────────────────────────────────────────────

def reference_pages(home):
    """Returns [(page_no, reference_png_path)] or None when there is no reference.
    PDF reference: per-page renders. Single image: page 1 only (the spec draws
    ONE similar slide from a single reference; the rest of the deck is
    topic-driven and has nothing to compare against)."""
    pdf_dir = os.path.join(home, ".hiq", "pdf-pages")
    if os.path.isdir(pdf_dir):
        pages = []
        for j in sorted(glob.glob(os.path.join(pdf_dir, "page-*.json"))):
            m = re.search(r"page-(\d+)\.json$", j)
            if not m:
                continue
            n = int(m.group(1))
            png = os.path.join(pdf_dir, "page-%d.png" % n)
            if os.path.isfile(png):
                pages.append((n, png))
        if pages:
            return pages
    ref_style = os.path.join(home, ".hiq", "reference-style.json")
    if os.path.isfile(ref_style):
        try:
            with open(ref_style, "r", encoding="utf-8") as f:
                style = json.load(f)
            src = str(style.get("source_path") or "").strip()
            if src and os.path.isfile(src):
                return [(1, src)]
        except (OSError, ValueError):
            pass
    return None


# ── main ────────────────────────────────────────────────────────────────────

def report(round_no, stop, reason, pages, note=""):
    return {
        "round": round_no,
        "stop": stop,
        "stop_reason": reason,
        "note": note,
        "pages": pages,
    }


def main():
    ap = argparse.ArgumentParser(description="Visual QA: compare generated SVG slides against the reference.")
    ap.add_argument("project_dir", help="ppt-auto project dir containing svg_output/")
    ap.add_argument("--round", type=int, default=1, help="rework round (1-based); script hard-caps at 2")
    ap.add_argument("--home", default=None, help="home dir containing .hiq/ (default: ~)")
    ap.add_argument("--sample", type=int, default=None,
                    help="QA only a subset of pages: the cover + N-1 evenly-spaced pages "
                         "(fast mode). Deterministic spacing so round-over-round no-progress "
                         "comparison stays meaningful. Default: all pages.")
    args = ap.parse_args()

    svg_dir = os.path.join(args.project_dir, "svg_output")
    # S-10: 统一枚举两种命名世代（slide_NN*.svg 与旧版 NN_type.svg）；
    # 只读用途——batch_check 的 fix 原地改写不在此扩围
    try:
        from project_utils import find_page_svgs
        svgs = find_page_svgs(svg_dir)
    except ImportError:
        svgs = sorted(glob.glob(os.path.join(svg_dir, "slide_*.svg")))
    report_path = os.path.join(args.project_dir, "qa-report.json")
    sampled = False
    keep = None
    if args.sample and args.sample > 0 and len(svgs) > args.sample:
        keep = {1}
        step = (len(svgs) - 1) / max(args.sample - 1, 1)
        for i in range(1, args.sample):
            keep.add(min(len(svgs), max(2, round(1 + i * step))))
        sampled = True

    def emit(rep):
        rep["done"] = True  # the resume gate treats done=false as a partial run
        rep["sampled"] = sampled
        with open(report_path, "w", encoding="utf-8") as f:
            json.dump(rep, f, ensure_ascii=False, indent=2)
        print(json.dumps(rep, ensure_ascii=False))
        return 0

    # Mechanical cap: never rework more than MAX_ROUNDS times.
    if args.round > MAX_ROUNDS:
        emit(report(args.round, True, "max_rounds", [],
                    "已到返工上限（%d 轮），接受当前结果" % MAX_ROUNDS))
        return 0
    if not svgs:
        emit(report(args.round, True, "no_svgs", []))
        return 0

    home = args.home or os.path.expanduser("~")
    refs = reference_pages(home)
    rubric_mode = not refs  # topic-driven decks: absolute-standard review, no reference

    # VLM availability check BEFORE rendering (M-6): rendering every page and
    # then discovering there is no key wastes the whole render pass.
    cfg, config_dir = load_hiq_config()
    vlm, why_not = (None, "vlm_config_missing")
    if cfg is not None:
        vlm, why_not = resolve_vlm(cfg, config_dir)
    if vlm is None:
        emit(report(args.round, True, why_not, [], "视觉 QA 跳过：%s" % why_not))
        return 0

    # Render the generated slides (render errors degrade per page, not globally).
    render_dir = os.path.join(args.project_dir, "qa-render")
    os.makedirs(render_dir, exist_ok=True)
    deck_bg = _deck_background(home)
    gen_pages = []  # (page_no, svg_path, png_path|None)
    for idx, svg in enumerate(svgs, start=1):
        if keep is not None and idx not in keep:
            continue
        png = os.path.join(render_dir, "slide_%02d.png" % idx)
        try:
            render_svg(svg, png)
            _flatten_alpha(png, deck_bg)
            gen_pages.append((idx, svg, png))
        except Exception as e:  # noqa: BLE001 — any render failure degrades that page
            gen_pages.append((idx, svg, None))

    # Pair reference pages with generated slides (page N ↔ slide N). In rubric
    # mode there is no reference: every generated page pairs with itself only
    # positionally (ref=None) and is judged by the absolute standard.
    pairs = []
    if rubric_mode:
        for idx, _, gen_png in gen_pages:
            pairs.append((idx, None, gen_png))
    else:
        covered = set()
        for n, ref_png in refs:
            if n <= len(gen_pages):
                covered.add(n)
                _, _, gen_png = gen_pages[n - 1]
                pairs.append((n, ref_png, gen_png))
        # Single-image reference (D-01): only page 1 resembles the reference;
        # the rest of the deck still gets QA via the absolute-standard rubric
        # instead of silently skipping visual review.
        for idx, _, gen_png in gen_pages:
            if idx not in covered:
                pairs.append((idx, None, gen_png))

    # ── Resumable rounds ──
    # The skill's bash tool kills commands at ~2 minutes; a 32-page compare
    # WILL be killed mid-run. So: seed verdicts from a PARTIAL report of the
    # SAME round, compare only the missing pages, and write the report after
    # EVERY page — a kill loses at most the in-flight page, and rerunning the
    # same command resumes instead of restarting.
    prior = None
    try:
        with open(report_path, "r", encoding="utf-8") as f:
            prior = json.load(f)
    except (OSError, ValueError):
        pass
    prev_sig = None  # captured BEFORE any writes (round>=2 no-progress baseline)
    if prior and args.round >= 2:
        prev_sig = [(p.get("page"), p.get("verdict"), tuple(p.get("issues") or []))
                    for p in prior.get("pages", [])]
    seeded = {}
    if prior and prior.get("round") == args.round and not prior.get("done"):
        seeded = {p.get("page"): p for p in prior.get("pages", []) if p.get("verdict")}

    def persist(pages, done, stop=False, reason=""):
        with open(report_path, "w", encoding="utf-8") as f:
            json.dump(report(args.round, stop, reason, pages, note="" if done else "in_progress"),
                      f, ensure_ascii=False, indent=2)

    results = [dict(seeded[n]) for n, _, _ in pairs if n in seeded]
    pending = [(n, ref, gen) for n, ref, gen in pairs
               if n not in seeded or not gen]
    def judge(n, ref, gen):
        gu = data_url(gen)
        return vlm_rubric(vlm, gu) if ref is None else vlm_compare(vlm, data_url(ref), gu)

    with concurrent.futures.ThreadPoolExecutor(max_workers=COMPARE_POOL) as pool:
        futures = {
            pool.submit(judge, n, ref, gen): n
            for n, ref, gen in pending if gen
        }
        for fut in concurrent.futures.as_completed(futures):
            n = futures[fut]
            try:
                verdict = fut.result()
            except Exception as e:  # noqa: BLE001 — one page's compare failure shouldn't sink the rest
                verdict = {"verdict": "PASS", "issues": ["QA 调用失败（按 PASS 处理）: %s" % e]}
            results.append({"page": n, **verdict})
            persist(sorted(results, key=lambda r: r["page"]), done=False)
    # Unrenderable pages: report as MINOR (visible, but never triggers rework —
    # a rendering gap is an environment issue, not a fidelity issue).
    rendered_ns = {n for n, _, gen in pairs if gen}
    for n, _, _ in pairs:
        if n not in rendered_ns and n not in seeded:
            results.append({"page": n, "verdict": "MINOR", "issues": ["SVG 渲染失败，未能对比"]})
    results.sort(key=lambda r: r["page"])

    # No-progress fuse: identical (page, verdict, issues) as the previous round
    # means the rework didn't move anything — stop instead of churning.
    stop = False
    reason = ""
    if prev_sig is not None:
        cur_sig = [(p.get("page"), p.get("verdict"), tuple(p.get("issues") or []))
                   for p in results]
        if prev_sig == cur_sig:
            stop, reason = True, "no_progress"
    # stop=False ⟺ there is MAJOR rework to do — keep the contract mechanical so
    # the agent never has to infer "no MAJOR means continue".
    if not stop and not any(p["verdict"] == "MAJOR" for p in results):
        stop, reason = True, "no_major"

    emit(report(args.round, stop, reason, results,
                note="rubric(absolute)" if rubric_mode else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
