"""Hyper-Extract HTTP server for Hiq integration.

Exposes Hyper-Extract's extraction capabilities over HTTP so the Go backend
can call it without Python dependency issues. Runs on localhost only.

Endpoints:
    GET  /health           - Health check
    GET  /templates        - List available templates
    POST /extract          - Extract entities/relations from text
    POST /search           - Semantic search over a KA
    POST /export_obsidian  - Export KA to Obsidian vault

Usage:
    python hyper_extract_server.py [--port 18900] [--host 127.0.0.1]
"""

import argparse
import json
import logging
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Dict, List, Optional

# Add Hyper-Extract to path if bundled.
HE_ROOT = Path(__file__).parent.parent / "Hyper-Extract"
if HE_ROOT.exists():
    sys.path.insert(0, str(HE_ROOT))

try:
    from http.server import HTTPServer, BaseHTTPRequestHandler
    from urllib.parse import urlparse, parse_qs
except ImportError:
    print("Python 3 stdlib http.server not available")
    sys.exit(1)

logger = logging.getLogger("he-server")

# ---------------------------------------------------------------------------
# Hyper-Extract integration
# ---------------------------------------------------------------------------

_he_available = False
try:
    from hyperextract.utils.template_engine import Gallery, Template
    from hyperextract.utils.client import get_client
    _he_available = True
except ImportError:
    logger.warning("Hyper-Extract not installed; extraction will be unavailable")


def _get_clients():
    """Get LLM and embedder clients from ~/.he/config.toml."""
    if not _he_available:
        raise RuntimeError("Hyper-Extract not installed")
    return get_client()


def _list_templates() -> List[Dict[str, Any]]:
    """List all available templates with metadata from YAML files."""
    if not _he_available:
        return [{"name": "general/graph", "description": "通用图谱 (Hyper-Extract not installed)", "available": False}]
    templates = []
    try:
        import yaml as pyyaml
        presets_root = Path(__file__).parent.parent / "Hyper-Extract" / "hyperextract" / "templates" / "presets"
        for preset_dir in presets_root.iterdir():
            if not preset_dir.is_dir():
                continue
            category = preset_dir.name
            for yaml_file in sorted(preset_dir.glob("*.yaml")):
                name = f"{category}/{yaml_file.stem}"
                meta: Dict[str, Any] = {
                    "name": name,
                    "category": category,
                    "file": str(yaml_file),
                    "available": True,
                    "description": "",
                    "templateType": "",
                    "entityFields": [],
                    "relationFields": [],
                }
                try:
                    with open(yaml_file, "r", encoding="utf-8") as f:
                        ym = pyyaml.safe_load(f)
                    if not ym:
                        templates.append(meta)
                        continue
                    # Description: prefer zh, fallback to en.
                    desc = ym.get("description", "")
                    if isinstance(desc, dict):
                        meta["description"] = desc.get("zh", desc.get("en", ""))
                    else:
                        meta["description"] = str(desc)
                    meta["templateType"] = ym.get("type", "")
                    # Entity fields.
                    output = ym.get("output", {})
                    ent_section = output.get("entities", {})
                    for field in ent_section.get("fields", []):
                        fname = field.get("name", "")
                        fdesc = field.get("description", "")
                        if isinstance(fdesc, dict):
                            fdesc = fdesc.get("zh", fdesc.get("en", ""))
                        meta["entityFields"].append({"name": fname, "description": str(fdesc)})
                    # Relation fields.
                    rel_section = output.get("relations", {})
                    for field in rel_section.get("fields", []):
                        fname = field.get("name", "")
                        fdesc = field.get("description", "")
                        if isinstance(fdesc, dict):
                            fdesc = fdesc.get("zh", fdesc.get("en", ""))
                        meta["relationFields"].append({"name": fname, "description": str(fdesc)})
                except Exception as e:
                    logger.warning(f"parse template yaml {yaml_file}: {e}")
                templates.append(meta)
    except Exception as e:
        logger.error(f"list_templates error: {e}")
    return templates


def _extract(text: str, template: str = "general/graph", lang: str = "zh") -> Dict[str, Any]:
    """Extract entities and relations from text using Hyper-Extract."""
    if not _he_available:
        raise RuntimeError("Hyper-Extract not installed")
    llm, emb = _get_clients()
    ka = Template.create(template, lang, llm_client=llm, embedder=emb)

    # Write text to temp file for extraction.
    with tempfile.NamedTemporaryFile(mode="w", suffix=".txt", delete=False, encoding="utf-8") as f:
        f.write(text)
        tmp_path = f.name

    try:
        result = ka.extract(tmp_path)
        # Convert to dict format compatible with Hiq's Entity/Relation types.
        entities = []
        relations = []
        if hasattr(result, "entities"):
            for e in result.entities:
                entities.append({
                    "name": getattr(e, "name", ""),
                    "type": getattr(e, "type", ""),
                    "description": getattr(e, "description", ""),
                })
        if hasattr(result, "relations"):
            for r in result.relations:
                relations.append({
                    "source": getattr(r, "source", ""),
                    "target": getattr(r, "target", ""),
                    "type": getattr(r, "type", "related_to"),
                    "description": getattr(r, "description", ""),
                })
        return {"entities": entities, "relations": relations}
    finally:
        os.unlink(tmp_path)


def _embed(texts: List[str]) -> Dict[str, Any]:
    """Generate embedding vectors for a list of texts."""
    if not _he_available:
        raise RuntimeError("Hyper-Extract not installed")
    _, emb = _get_clients()
    vectors = emb.embed_documents(texts)
    return {"vectors": vectors}


def _summarize(entities: List[Dict], relations: List[Dict], lang: str = "zh") -> Dict[str, Any]:
    """Generate a knowledge summary from entities and relations."""
    if not _he_available:
        raise RuntimeError("Hyper-Extract not installed")
    llm, _ = _get_clients()

    # Format entities and relations as context.
    ent_lines = []
    for e in entities:
        name = e.get("name", "")
        typ = e.get("type", "")
        desc = e.get("description", "")
        ent_lines.append(f"- {name} ({typ}): {desc}")
    rel_lines = []
    for r in relations:
        src = r.get("source", "")
        tgt = r.get("target", "")
        typ = r.get("type", "related_to")
        desc = r.get("description", "")
        rel_lines.append(f"- {src} →[{typ}]→ {tgt}: {desc}")

    context = "实体：\n" + "\n".join(ent_lines[:100])  # cap at 100 for prompt size
    if rel_lines:
        context += "\n\n关系：\n" + "\n".join(rel_lines[:100])

    lang_hint = "请用中文回复。" if lang == "zh" else "Please reply in English."
    prompt = f"""请根据以下知识图谱的实体和关系，生成一段简洁的摘要（200字以内），并提取3-5个关键主题标签。{lang_hint}

{context}

请用JSON格式回复：
{{"summary": "摘要文本", "themes": ["主题1", "主题2", ...]}}"""

    try:
        # Call LLM directly — works with any LangChain BaseChatModel.
        response = llm.invoke([{"role": "user", "content": prompt}])
        content = response.content.strip()
        import re
        json_match = re.search(r'\{.*\}', content, re.DOTALL)
        if json_match:
            result = json.loads(json_match.group())
            return {"summary": result.get("summary", ""), "themes": result.get("themes", [])}
        return {"summary": content, "themes": []}
    except Exception as e:
        logger.error(f"summarize error: {e}")
        return {"summary": "", "themes": [], "error": str(e)}


def _export_obsidian(ka_path: str, output_dir: str) -> Dict[str, Any]:
    """Export a KA to Obsidian vault."""
    if not _he_available:
        raise RuntimeError("Hyper-Extract not installed")
    llm, emb = _get_clients()
    ka = Template.load_from_disk(ka_path, llm_client=llm, embedder=emb)
    ka.export_obsidian(output_dir)
    return {"success": True, "output_dir": output_dir}


# ---------------------------------------------------------------------------
# HTTP Handler
# ---------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        logger.info(format % args)

    def _send_json(self, data: Any, status: int = 200):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(data, ensure_ascii=False).encode())

    def _read_body(self) -> bytes:
        length = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(length)

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/health":
            self._send_json({"status": "ok", "he_available": _he_available})
        elif parsed.path == "/templates":
            self._send_json({"templates": _list_templates()})
        else:
            self._send_json({"error": "not found"}, 404)

    def do_POST(self):
        parsed = urlparse(self.path)
        body = self._read_body()

        try:
            data = json.loads(body) if body else {}
        except json.JSONDecodeError:
            self._send_json({"error": "invalid JSON"}, 400)
            return

        try:
            if parsed.path == "/extract":
                text = data.get("text", "")
                template = data.get("template", "general/graph")
                lang = data.get("lang", "zh")
                result = _extract(text, template, lang)
                self._send_json(result)
            elif parsed.path == "/export_obsidian":
                ka_path = data.get("ka_path", "")
                output_dir = data.get("output_dir", "")
                result = _export_obsidian(ka_path, output_dir)
                self._send_json(result)
            elif parsed.path == "/embed":
                texts = data.get("texts", [])
                if not texts:
                    self._send_json({"error": "texts required"}, 400)
                    return
                result = _embed(texts)
                self._send_json(result)
            elif parsed.path == "/summarize":
                entities = data.get("entities", [])
                relations = data.get("relations", [])
                lang = data.get("lang", "zh")
                result = _summarize(entities, relations, lang)
                self._send_json(result)
            else:
                self._send_json({"error": "not found"}, 404)
        except Exception as e:
            logger.error(f"POST {parsed.path} error: {e}")
            self._send_json({"error": str(e)}, 500)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Hyper-Extract HTTP Server")
    parser.add_argument("--host", default="127.0.0.1", help="Bind host")
    parser.add_argument("--port", type=int, default=18900, help="Bind port")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")

    server = HTTPServer((args.host, args.port), Handler)
    logger.info(f"Hyper-Extract server listening on {args.host}:{args.port}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down...")
        server.shutdown()


if __name__ == "__main__":
    main()
