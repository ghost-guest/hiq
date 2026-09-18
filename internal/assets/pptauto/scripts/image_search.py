#!/usr/bin/env python3
"""Keyless web image search via Baidu Images (hiq multi-user default).

Usage:
    python image_search.py --query "数据中心 机房" --out <project>/images/hero.png
    python image_search.py --query "cloud computing" --dir <project>/images --n 3
    python image_search.py --query "server rack" --min-width 800 --aspect landscape

WHY Baidu: hiq ships to many users who must NOT be asked to register API
keys. Bing's HTML serves junk to cookie-less clients (verified: relevant
queries returned cached celebrity/shop images); Baidu's acjson API returns
relevant results after a BAIDUID cookie warm-up on www.baidu.com — no key, no
registration, strongest Chinese coverage. Keyed sources (Pexels/Pixabay) can
be added later as optional upgrades; icons/SVG self-drawing stay the FIRST
choice, this fills the "topic-driven deck needs a photo" tier.

Anti-spider notes: acjson rejects bare requests ("Forbid spider access"), so
we warm a session on www.baidu.com first and reuse the cookies; a timestamp
cache-buster and Referer are included. The image host itself is flaky at TLS
handshake sometimes — downloads retry once. Candidates are Pillow-validated
(real image, min width, optional aspect); the original URL (objURL) is
preferred, falling back to the Baidu CDN thumbnail when hotlink-protected.
Output is a JSON summary; failures degrade to a clear error, never a block.
"""

from __future__ import annotations

import argparse
import io
import json
import os
import sys
import time
import urllib.parse
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

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/126.0 Safari/537.36")
TIMEOUT = 15
MAX_IMAGE_BYTES = 15 * 1024 * 1024


def _get(url, headers, tries=2):
    last = None
    for i in range(tries):
        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
                return r.read()
        except Exception as e:  # noqa: BLE001 — flaky TLS: retry then give up
            last = e
            time.sleep(0.8)
    raise last


def _baidu_session():
    """Warm www.baidu.com for the BAIDUID cookie (domain-wide, gates acjson)."""
    req = urllib.request.Request("https://www.baidu.com/", headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=10) as r:
        jars = r.headers.get_all("Set-Cookie") or []
    return "; ".join(c.split(";", 1)[0] for c in jars if "=" in c)


def baidu_candidates(query, limit=16):
    """acjson search -> [(orig_url, thumb_url, title, w, h), ...]."""
    cookie = _baidu_session()
    q = urllib.parse.quote(query)
    url = ("https://image.baidu.com/search/acjson?tn=resultjson_com&ipn=rj&ct=201326592"
           "&fp=result&queryWord=%s&word=%s&cl=2&ie=utf-8&oe=utf-8"
           "&face=0&istype=2&pn=0&rn=%d&t=%d" % (q, q, limit + 6, int(time.time() * 1000) % 10**7))
    body = _get(url, {"User-Agent": UA, "Cookie": cookie,
                      "Referer": "https://image.baidu.com/",
                      "Accept": "application/json"}).decode("utf-8", "replace")
    data = json.loads(body)
    out = []
    for d in data.get("data", []):
        if not d:
            continue
        orig = str(d.get("objURL") or "")
        thumb = str(d.get("thumbURL") or "")
        if not (orig.startswith("http") or thumb.startswith("http")):
            continue
        out.append({
            "orig": orig, "thumb": thumb,
            "title": str(d.get("fromPageTitleEnc") or "")[:80],
            "w": int(d.get("width") or 0), "h": int(d.get("height") or 0),
        })
        if len(out) >= limit:
            break
    return out


def valid_image(data, min_width, aspect):
    try:
        from PIL import Image
        Image.MAX_IMAGE_PIXELS = 80_000_000
        im = Image.open(io.BytesIO(data))
        w, h = im.size
        if w < min_width or h < 160:
            return None
        if aspect == "landscape" and w < h * 1.15:
            return None
        if aspect == "square" and not (0.75 < w / h < 1.35):
            return None
        im.load()
        return (w, h, (im.format or "PNG").upper())
    except Exception:  # noqa: BLE001
        return None


def main():
    ap = argparse.ArgumentParser(description="Keyless Baidu image search for ppt-auto.")
    ap.add_argument("--query", required=True)
    ap.add_argument("--out", default=None, help="output file path (single image)")
    ap.add_argument("--dir", default=None, help="output dir (writes image_1.png ...)")
    ap.add_argument("--n", type=int, default=1, help="how many images to save (with --dir)")
    ap.add_argument("--min-width", type=int, default=500)
    ap.add_argument("--aspect", choices=["any", "landscape", "square"], default="any")
    args = ap.parse_args()

    def emit(obj):
        print(json.dumps(obj, ensure_ascii=False))
        return 0  # always 0 — a failed search must never block the deck

    if not args.out and not args.dir:
        return emit({"error": "need --out or --dir"})
    try:
        cands = baidu_candidates(args.query)
    except Exception as e:  # noqa: BLE001
        return emit({"error": "search failed: %s" % e, "query": args.query})
    if not cands:
        return emit({"error": "no results", "query": args.query})

    want = args.n if args.dir else 1
    saved = []
    for c in cands:
        if len(saved) >= want:
            break
        for kind, url in (("orig", c["orig"]), ("thumb", c["thumb"])):
            if not url:
                continue
            try:
                data = _get(url, {"User-Agent": UA, "Referer": "https://image.baidu.com/"}, tries=2)
            except Exception:  # noqa: BLE001 — hotlink-protected host: next
                continue
            if len(data) > MAX_IMAGE_BYTES:
                continue
            mw = args.min_width if kind == "orig" else max(240, args.min_width // 2)
            meta = valid_image(data, mw, args.aspect)
            if not meta:
                continue
            path = args.out if args.out else os.path.join(args.dir, "image_%d.png" % (len(saved) + 1))
            os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
            with open(path, "wb") as f:
                f.write(data)
            saved.append({"path": path, "w": meta[0], "h": meta[1], "format": meta[2],
                          "quality": kind, "title": c["title"]})
            break

    if not saved:
        return emit({"error": "no valid downloadable image", "query": args.query,
                     "candidates": len(cands)})
    return emit({"query": args.query, "saved": saved, "candidates": len(cands)})


if __name__ == "__main__":
    sys.exit(main())
