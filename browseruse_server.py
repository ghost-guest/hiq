#!/usr/bin/env python3
"""browseruse_server.py — local HTTP sidecar that runs a browser-use Agent.

The hiq Go host launches a system Chromium-based browser (Chrome / Edge)
itself (see internal/browserlaunch), hands this server a CDP wsURL via the
/run endpoint, and this server drives that very browser with a browser-use
agentic loop. There is exactly ONE shared browser instance: the agent drives
it, the in-app panel mirrors it via CDP screencast.

Protocol (mirrors the Hyper-Extract server so the two sidecars feel uniform):

    GET  /health  -> {"ok": bool, "browser_use_available": bool}
    POST /run     -> text/event-stream of step events:
                     data: {"type":"thought","step":N,"text":"..."}
                     data: {"type":"action","step":N,"text":"goto ..."}
                     data: {"type":"screenshot","step":N,"image":"data:..."}
                     data: {"type":"done","done":true,"text":"final summary"}
                     data: {"type":"error","done":true,"text":"..."}
    POST /stop    -> {"ok": true}  (cancels the in-flight run, if any)

LLM credentials are inherited from the parent environment (the Go host runs
loadDotEnv at boot and spawns this process without overriding the env, so
OPENAI_API_KEY / ANTHROPIC_API_KEY etc. are present). The model + base_url +
proxy come per-request from the host so the sidecar uses the same provider the
user configured in hiq.

Run standalone for debugging:
    python browseruse_server.py --port 18901 --host 127.0.0.1
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import json
import os
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# --- browser-use availability probe (lazy, hard failure handled gracefully) ---
BU_IMPORT_ERROR: str | None = None
try:
    from browser_use import Agent, Browser, BrowserProfile  # type: ignore

    # LLM clients are optional depending on which provider the host selects.
    _llm_clients: dict = {}
    try:
        from browser_use import ChatOpenAI  # type: ignore

        _llm_clients["openai"] = ChatOpenAI
    except Exception:  # pragma: no cover - optional
        pass
    try:
        from browser_use import ChatAnthropic  # type: ignore

        _llm_clients["anthropic"] = ChatAnthropic
    except Exception:  # pragma: no cover - optional
        pass
except Exception as exc:  # pragma: no cover - keeps the server alive if deps missing
    BU_IMPORT_ERROR = f"{type(exc).__name__}: {exc}"
    Agent = None  # type: ignore


# --- run state (single in-flight run at a time) ------------------------------
# A threading.Event signals the agent loop to stop. browser-use has a
# pause()/resume() API but no clean external cancel, so we raise a custom
# exception from the step hook to break out of run().
class _Cancelled(Exception):
    pass


_run_lock = threading.Lock()
_cancel_event = threading.Event()


class RunSpec:
    """Parsed POST /run body."""

    def __init__(self, payload: dict) -> None:
        self.goal: str = (payload.get("goal") or "").strip()
        self.url: str = (payload.get("url") or "").strip()
        self.cdp_url: str = (payload.get("cdp_url") or "").strip()
        self.max_steps: int = int(payload.get("max_steps") or 0)
        self.model: str = payload.get("model") or ""
        # provider_kind: "openai" (default) or "anthropic". Selects the client
        # family deterministically rather than guessing from the model name.
        self.provider_kind: str = (payload.get("provider_kind") or "").strip().lower()
        self.base_url: str = payload.get("base_url") or ""
        # api_key_env: which env var holds the key. The host passes the hiq
        # provider's api_key_env name (e.g. OPENAI_API_KEY); we surface it as
        # the standard OPENAI_API_KEY/ANTHROPIC_API_KEY the chat clients read.
        self.api_key_env: str = (payload.get("api_key_env") or "").strip()
        self.proxy: str = payload.get("proxy") or ""


def _resolve_api_key(spec: RunSpec, default_env: str) -> str:
    """Return the API key for the resolved provider.

    Preference: the env var named by api_key_env (the hiq provider's key),
    then the standard default_env (OPENAI_API_KEY / ANTHROPIC_API_KEY), then "".
    Also surface the resolved key as default_env in os.environ so LangChain chat
    clients (which hard-read os.environ[default_env]) pick it up.
    """
    key = ""
    if spec.api_key_env:
        key = os.environ.get(spec.api_key_env, "")
    if not key:
        key = os.environ.get(default_env, "")
    if key:
        os.environ[default_env] = key
    return key


def build_llm(spec: RunSpec):
    """Construct the LLM client from the per-request model/base_url/key.

    browser-use accepts LangChain-style chat models. The host resolves hiq's
    "provider/model" ref into (bare model name, base_url, provider_kind, api_key_env)
    so a custom gateway is driven correctly instead of hitting the
    default api.openai.com. Keys come from the environment, never over the wire.
    """
    model = spec.model or ""
    # Choose client family: explicit provider_kind wins; otherwise infer from
    # the model name (claude → anthropic); otherwise default to openai.
    is_anthropic = spec.provider_kind == "anthropic" or (
        spec.provider_kind == "" and "claude" in model.lower()
    )
    # When a proxy is configured, build an httpx async client that routes
    # through it, so the sidecar's LLM calls reach a gateway only reachable via
    # the user's network proxy (e.g. a CN proxy). browser-use's ChatOpenAI /
    # ChatAnthropic accept an http_client kwarg.
    http_client = None
    if spec.proxy:
        try:
            import httpx  # type: ignore

            http_client = httpx.AsyncClient(proxy=spec.proxy)
        except Exception:
            http_client = None  # best-effort; fall back to direct
    if is_anthropic and "anthropic" in _llm_clients:
        _resolve_api_key(spec, "ANTHROPIC_API_KEY")
        kwargs = {"model": model} if model else {}
        if http_client is not None:
            kwargs["http_client"] = http_client
        return _llm_clients["anthropic"](**kwargs)
    # Default: OpenAI-compatible (covers OpenAI, Azure-compatible, and
    # any OpenAI-compatible gateway via base_url).
    if "openai" in _llm_clients:
        _resolve_api_key(spec, "OPENAI_API_KEY")
        kwargs = {}
        if model:
            kwargs["model"] = model
        if spec.base_url:
            kwargs["base_url"] = spec.base_url
        if http_client is not None:
            kwargs["http_client"] = http_client
        return _llm_clients["openai"](**kwargs)
    raise RuntimeError(
        "no LLM client available (install browser-use with the desired provider)"
    )


def emit_event(wfile, event: dict) -> None:
    """Write one SSE event to the response stream."""
    data = json.dumps(event, ensure_ascii=False)
    line = "data: " + data + "\n\n"
    wfile.write(line.encode("utf-8"))
    wfile.flush()


def run_agent_sync(spec: RunSpec, wfile) -> None:
    """Run the browser-use agent synchronously, streaming events to wfile.

    browser-use is async, so we drive its loop on a dedicated thread. Events
    are emitted from the step hooks back onto this thread via a queue, then
    written to the SSE stream. This avoids blocking the event loop on slow
    socket writes (base64 screenshots can be large).
    """
    import queue

    event_q: "queue.Queue[dict | None]" = queue.Queue()
    loop = asyncio.new_event_loop()

    def put(ev: dict) -> None:
        event_q.put(ev)

    async def main() -> None:
        profile = BrowserProfile(cdp_url=spec.cdp_url)
        browser = Browser(browser_profile=profile)
        llm = build_llm(spec)

        async def on_step(agent):
            # Check cancellation first — raising here unwinds agent.run().
            if _cancel_event.is_set():
                raise _Cancelled()
            step = getattr(getattr(agent, "state", None), "n_steps", 0) or 0
            # Push the latest thought + action text to the stream.
            # NOTE: browser-use's AgentBrain fields vary across versions. We read
            # the common fields defensively so this never crashes (wrapped in
            # try/except) and degrades to a best-effort string.
            try:
                thoughts = agent.history.model_thoughts()
                if thoughts:
                    last = thoughts[-1]
                    # AgentBrain (browser-use 0.13.x) has thinking /
                    # evaluation_previous_goal / next_goal. Older versions used
                    # reasoning / think. Fall through them all.
                    text = (
                        getattr(last, "next_goal", None)
                        or getattr(last, "thinking", None)
                        or getattr(last, "evaluation_previous_goal", None)
                        or getattr(last, "reasoning", None)
                        or getattr(last, "think", None)
                    )
                    if text:
                        put({"type": "thought", "step": step, "text": str(text)})
            except Exception:
                pass
            try:
                actions = agent.history.model_actions()
                if actions:
                    last = actions[-1]
                    put({"type": "action", "step": step, "text": _describe_action(last)})
            except Exception:
                pass
            # Push a screenshot so the host panel can show the page (in
            # addition to the host's own CDP screencast — this one is the
            # agent's own view, useful as a sanity check).
            try:
                img = await _take_screenshot(agent)
                if img:
                    put({"type": "screenshot", "step": step, "image": img})
            except Exception:
                pass
            # Visualize the action: inject a transient page-wide banner so a
            # human watching the host's screencast mirror can see "the agent
            # just acted". Borrowed from agent-browser's DOM-injection approach
            # (Runtime.evaluate rather than the CDP Overlay domain) — and since
            # the host mirrors the page via screencast, the injected marker
            # appears in the panel automatically, no client-side overlay needed.
            try:
                await _flash_action(agent, step)
            except Exception:
                pass

        run_kwargs = {"on_step_end": on_step}
        if spec.max_steps and spec.max_steps > 0:
            run_kwargs["max_steps"] = spec.max_steps

        agent = Agent(task=spec.goal, llm=llm, browser=browser)
        # Optional starting URL: navigate before the loop so the first step is
        # already on the right page.
        if spec.url:
            try:
                session = await agent.browser_session.get_or_create_cdp_session()
                await session.cdp_client.send.Page.navigate(
                    params={"url": spec.url}, session_id=session.session_id
                )
            except Exception:
                # Non-fatal: the agent can still navigate as a step.
                pass

        try:
            result = await agent.run(**run_kwargs)
            summary = _final_summary(result)
            put({"type": "done", "done": True, "text": summary})
        except _Cancelled:
            put({"type": "done", "done": True, "text": "run cancelled"})
        except Exception as exc:  # surface the failure to the host
            put({"type": "error", "done": True, "text": f"{type(exc).__name__}: {exc}\n{traceback.format_exc()}"})
        finally:
            try:
                await browser.close()
            except Exception:
                pass

    def worker() -> None:
        try:
            loop.run_until_complete(main())
        except Exception as exc:
            put({"type": "error", "done": True, "text": f"loop crashed: {exc}"})
        finally:
            loop.close()
            put(None)  # sentinel: stream is over

    th = threading.Thread(target=worker, daemon=True)
    th.start()

    # Drain the queue onto the SSE stream until the sentinel arrives.
    while True:
        ev = event_q.get()
        if ev is None:
            break
        emit_event(wfile, ev)


def _describe_action(action_obj) -> str:
    """Render an agent action history item as a short human description.

    browser-use's model_actions() returns a list of dicts (in 0.13.x) shaped like
    {'goto_url': {'url': '...'}} or {'click_element': {'index': 5, ...}} — one
    action name → its params. We format it as 'name(params)'. Older versions
    returned objects with an `interaction` attribute; we handle both.
    """
    # Dict form (browser-use 0.13.x): {action_name: params_dict}.
    if isinstance(action_obj, dict):
        parts = []
        for name, params in action_obj.items():
            if isinstance(params, dict) and params:
                # Compact 'key=value' pairs, skipping empty/unset values.
                kv = ", ".join(
                    f"{k}={v!r}" for k, v in params.items()
                    if v not in (None, "", [], {})
                )
                parts.append(f"{name}({kv})" if kv else name)
            else:
                parts.append(str(name))
        return " | ".join(parts) if parts else ""
    # Object form (older versions): try common attributes.
    for attr in ("interaction", "action", "name"):
        v = getattr(action_obj, attr, None)
        if v:
            return str(v)
    return str(action_obj)


async def _flash_action(agent, step: int) -> None:
    """Inject a transient banner + outline so a watching human sees the agent act.

    Borrowed from agent-browser's DOM-injection visualization: rather than the
    CDP Overlay domain (which neither agent-browser nor OpenWork use), we inject
    a short-lived element via Runtime.evaluate. Because the host mirrors the
    page via CDP screencast, the marker appears in the in-app panel
    automatically — no client-side overlay plumbing required. The banner
    auto-clears after a few seconds.
    """
    js = (
        "(() => {"
        "  var id = '__bu_action_flash__';"
        "  var e = document.getElementById(id); if (e) e.remove();"
        "  var b = document.createElement('div'); b.id = id;"
        "  b.textContent = 'AI step " + str(step) + " ▸ action';"
        "  b.style.cssText = 'position:fixed;top:8px;left:50%;transform:translateX(-50%);"
        "    background:rgba(37,99,235,0.92);color:#fff;font:bold 12px/1.4 system-ui,sans-serif;"
        "    padding:5px 12px;border-radius:6px;z-index:2147483647;pointer-events:none;"
        "    box-shadow:0 2px 8px rgba(0,0,0,0.3);transition:opacity .3s;opacity:1;';"
        "  document.documentElement.appendChild(b);"
        "  setTimeout(() => { b.style.opacity='0'; setTimeout(()=>b.remove(), 350); }, 2500);"
        "  return true;"
        "})()"
    )
    try:
        session = await agent.browser_session.get_or_create_cdp_session()
        await session.cdp_client.send.Runtime.evaluate(
            params={"expression": js}, session_id=session.session_id
        )
    except Exception:
        pass


async def _take_screenshot(agent) -> str | None:
    """Capture a viewport screenshot as a data URL via the browser session."""
    try:
        from browser_use.browser.events import ScreenshotEvent  # type: ignore

        ev = agent.browser_session.event_bus.dispatch(ScreenshotEvent(full_page=False))
        await ev
        result = await ev.event_result(raise_if_any=True, raise_if_none=True)
        # result may be bytes or a base64 string depending on version.
        if isinstance(result, (bytes, bytearray)):
            b64 = base64.b64encode(bytes(result)).decode("ascii")
            return "data:image/png;base64," + b64
        if isinstance(result, str):
            if result.startswith("data:"):
                return result
            return "data:image/png;base64," + result
    except Exception:
        return None
    return None


def _final_summary(result) -> str:
    """Build a concise final summary from the AgentHistoryList result."""
    try:
        steps = getattr(result, "number_of_steps", None)
        done = getattr(result, "is_done", None)
        if callable(done):
            done = done()
        parts = [f"steps={steps}", f"done={done}"]
        # Final result text, if any.
        final = getattr(result, "final_result", None)
        if callable(final):
            final = final()
        if final:
            parts.append("result=" + str(final))
        return " ".join(parts)
    except Exception:
        return "run finished"


# --- HTTP server -------------------------------------------------------------
class Handler(BaseHTTPRequestHandler):
    # Suppress the default per-request stderr logging (the host pipes our
    # stdout/stderr into its own logs; we don't want request noise there).
    def log_message(self, format, *args):  # noqa: A002
        return

    def _send_json(self, code: int, obj: dict) -> None:
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        if self.path == "/health":
            self._send_json(
                200,
                {"ok": True, "browser_use_available": BU_IMPORT_ERROR is None},
            )
            return
        self._send_json(404, {"ok": False, "error": "not found"})

    def do_POST(self):  # noqa: N802
        if self.path == "/stop":
            _cancel_event.set()
            self._send_json(200, {"ok": True})
            return

        if self.path != "/run":
            self._send_json(404, {"ok": False, "error": "not found"})
            return

        if BU_IMPORT_ERROR is not None:
            self._send_json(
                500,
                {"ok": False, "error": "browser-use not available: " + BU_IMPORT_ERROR},
            )
            return

        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw.decode("utf-8") or "{}")
        except Exception as exc:
            self._send_json(400, {"ok": False, "error": "bad json: " + str(exc)})
            return

        spec = RunSpec(payload)
        if not spec.goal:
            self._send_json(400, {"ok": False, "error": "missing 'goal'"})
            return
        if not spec.cdp_url:
            self._send_json(400, {"ok": False, "error": "missing 'cdp_url'"})
            return

        # Only one run at a time. If a previous run is in flight, reject so the
        # host gets a clear signal rather than queuing behind a long task.
        if not _run_lock.acquire(blocking=False):
            self._send_json(409, {"ok": False, "error": "a run is already in progress"})
            return

        _cancel_event.clear()
        # Begin the SSE response.
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        try:
            run_agent_sync(spec, self.wfile)
        except Exception as exc:
            emit_event(self.wfile, {"type": "error", "done": True, "text": str(exc)})
        finally:
            _run_lock.release()


def main() -> None:
    parser = argparse.ArgumentParser(description="browser-use sidecar server")
    parser.add_argument("--port", type=int, default=18901)
    parser.add_argument("--host", default="127.0.0.1")
    args = parser.parse_args()

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"[browseruse] listening on http://{args.host}:{args.port} "
          f"(browser_use_available={BU_IMPORT_ERROR is None})", flush=True)
    if BU_IMPORT_ERROR:
        print(f"[browseruse] WARNING browser-use import failed: {BU_IMPORT_ERROR}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
