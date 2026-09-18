"""
Unified desktop interaction bridge for hiq.

Called by Go via subprocess. All actions use JSON stdin/stdout protocol.

Usage:
  python desktop_bridge.py <action> [--args '{"key":"value"}']

Actions: uia_dump, click, type_text, press_keys, scroll, focus_window, close_window, screenshot
"""
import sys
import io
import json
import argparse
import time
import random

# Force UTF-8 stdout (Windows defaults to GBK)
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

# --- Dependencies ---
try:
    import pyautogui
    pyautogui.FAILSAFE = False
    HAS_PYAUTO = True
except ImportError:
    HAS_PYAUTO = False

try:
    import pyperclip
    HAS_CLIP = True
except ImportError:
    HAS_CLIP = False

try:
    import win32gui
    import win32con
    import win32api
    HAS_WIN32 = True
except ImportError:
    HAS_WIN32 = False

try:
    from pywinauto import Desktop
    HAS_PYWINAUTO = True
except ImportError:
    HAS_PYWINAUTO = False


def ok(data):
    print(json.dumps({"ok": True, **data}, ensure_ascii=False))

def fail(msg):
    print(json.dumps({"ok": False, "error": msg}, ensure_ascii=False))
    sys.exit(1)


# --- Actions ---

def action_uia_dump(args):
    """UIA element dump via pywinauto."""
    if not HAS_PYWINAUTO:
        fail("pywinauto not installed")

    screen_w = args.get("screen_width", 1920)
    screen_h = args.get("screen_height", 1080)

    desktop = Desktop(backend="uia")
    seen = {}
    elements = []

    for win in desktop.windows():
        try:
            if not win.is_visible():
                continue
            hwnd = win.handle

            for i, ctrl in enumerate(win.descendants()):
                if i > 2500:
                    break
                try:
                    c_info = ctrl.element_info
                    c_type = str(c_info.control_type).split(".")[-1]
                    name = (ctrl.window_text() or c_info.name or "").strip()

                    r = ctrl.rectangle()
                    if r.width() <= 3 or r.height() <= 3:
                        continue

                    x1, y1, x2, y2 = r.left, r.top, r.right, r.bottom
                    cx = x1 + (x2 - x1) // 2
                    cy = y1 + (y2 - y1) // 2

                    if cx < 0 or cy < 0 or cx > screen_w or cy > screen_h:
                        continue

                    is_container = c_type in {
                        "Pane", "Group", "List", "Tab", "ToolBar",
                        "Window", "Custom", "Document", "Table", "Tree", "MenuBar",
                    }
                    grid_key = f"{cx // 15}_{cy // 15}"
                    area = (x2 - x1) * (y2 - y1)
                    node = {
                        "name": name or c_type,
                        "type": c_type,
                        "box": [x1, y1, x2, y2],
                        "center": [cx, cy],
                        "hwnd": hwnd,
                        "is_enabled": ctrl.is_enabled(),
                        "is_container": is_container,
                    }

                    if grid_key in seen:
                        if area < seen[grid_key]["_area"]:
                            node["_area"] = area
                            seen[grid_key] = node
                    else:
                        node["_area"] = area
                        seen[grid_key] = node
                except Exception:
                    continue
        except Exception:
            continue

    result = []
    for node in seen.values():
        node.pop("_area", None)
        result.append(node)
    ok({"elements": result, "count": len(result)})


def action_click(args):
    """Mouse click via pyautogui with human-like jitter."""
    if not HAS_PYAUTO:
        fail("pyautogui not installed")

    x = args["x"]
    y = args["y"]
    button = args.get("button", "left")
    double = args.get("double", False)

    # Force focus the window at click position
    if HAS_WIN32:
        try:
            hwnd = win32gui.WindowFromPoint((x, y))
            if hwnd:
                root = win32gui.GetAncestor(hwnd, win32con.GA_ROOT)
                if win32gui.GetForegroundWindow() != root:
                    win32gui.ShowWindow(root, win32con.SW_RESTORE)
                    ctypes.windll.user32.SetForegroundWindow(root)
                    time.sleep(0.05)
        except Exception:
            pass

    # Human-like micro-offset
    ox = x + random.choice([-1, 0, 1])
    oy = y + random.choice([-1, 0, 1])
    hold = random.uniform(0.04, 0.09)

    if double:
        pyautogui.click(ox, oy, clicks=2, interval=0.12, button=button)
    else:
        pyautogui.mouseDown(ox, oy, button=button)
        time.sleep(hold)
        pyautogui.mouseUp(ox, oy, button=button)

    ok({"clicked": [ox, oy], "button": button, "double": double})


def _focus_window_by_title(title):
    """Focus a window by title substring. Used by type_text and press_keys."""
    if not HAS_WIN32 or not title:
        return
    try:
        title_lower = title.lower()
        found = None
        def enum_cb(hwnd, _):
            nonlocal found
            if found:
                return
            if win32gui.IsWindowVisible(hwnd):
                t = win32gui.GetWindowText(hwnd)
                if title_lower in t.lower():
                    found = hwnd
        win32gui.EnumWindows(enum_cb, None)
        if found:
            if win32gui.IsIconic(found):
                win32gui.ShowWindow(found, win32con.SW_RESTORE)
                time.sleep(0.1)
            try:
                fg = win32gui.GetForegroundWindow()
                fg_tid = win32api.GetWindowThreadProcessId(fg)[0] if fg else 0
                target_tid = win32api.GetWindowThreadProcessId(found)[0]
                cur_tid = win32api.GetCurrentThreadId()
                if fg_tid != target_tid:
                    ctypes.windll.user32.AttachThreadInput(cur_tid, target_tid, True)
                    win32gui.BringWindowToTop(found)
                    win32gui.SetForegroundWindow(found)
                    ctypes.windll.user32.AttachThreadInput(cur_tid, target_tid, False)
                else:
                    win32gui.SetForegroundWindow(found)
            except Exception:
                win32gui.SetForegroundWindow(found)
            time.sleep(0.15)
    except Exception:
        pass


def action_type_text(args):
    """Type text via clipboard + Ctrl+V (works with Win11 Notepad).
    Optional window_title: focus that window before typing.
    """
    if not HAS_PYAUTO:
        fail("pyautogui not installed")

    text = args["text"]
    press_enter = args.get("press_enter", False)
    window_title = args.get("window_title", "")

    if not text:
        fail("text is empty")

    # Focus window first if specified.
    _focus_window_by_title(window_title)

    use_clipboard = HAS_CLIP and (len(text) > 5 or any(ord(c) > 127 for c in text))

    if use_clipboard:
        pyperclip.copy(text)
        time.sleep(0.1)
        pyautogui.hotkey("ctrl", "v")
        time.sleep(0.2)
    else:
        pyautogui.typewrite(text, interval=0.03)

    if press_enter:
        time.sleep(0.05)
        pyautogui.press("enter")

    ok({"typed": len(text), "via_clipboard": use_clipboard})


def action_press_keys(args):
    """Press key combination via pyautogui. Optional window_title: focus first."""
    if not HAS_PYAUTO:
        fail("pyautogui not installed")

    keys_str = args["keys"]
    window_title = args.get("window_title", "")

    # Focus window first if specified.
    if window_title and HAS_WIN32:
        _focus_window_by_title(window_title)

    # Parse "ctrl+s" -> ["ctrl", "s"]
    keys = [k.strip().lower() for k in keys_str.replace("-", "+").split("+") if k.strip()]

    if len(keys) > 1:
        # Combo: hold modifiers, press key, release
        for k in keys[:-1]:
            pyautogui.keyDown(k)
            time.sleep(random.uniform(0.02, 0.05))
        pyautogui.press(keys[-1])
        time.sleep(random.uniform(0.01, 0.03))
        for k in reversed(keys[:-1]):
            pyautogui.keyUp(k)
            time.sleep(random.uniform(0.01, 0.03))
    else:
        # Single key
        hold = random.uniform(0.03, 0.08)
        pyautogui.keyDown(keys[0])
        time.sleep(hold)
        pyautogui.keyUp(keys[0])

    ok({"pressed": keys_str})


def action_scroll(args):
    """Mouse scroll via pyautogui."""
    if not HAS_PYAUTO:
        fail("pyautogui not installed")

    x = args.get("x", 960)
    y = args.get("y", 540)
    amount = args.get("amount", 3)

    pyautogui.moveTo(x, y, duration=0.1)
    time.sleep(0.05)
    pyautogui.scroll(-amount * 100)  # pyautogui uses positive=up

    direction = "down" if amount > 0 else "up"
    ok({"scrolled": direction, "amount": abs(amount), "at": [x, y]})


def action_focus_window(args):
    """Focus a window by title substring."""
    if not HAS_WIN32:
        fail("win32gui not installed")

    title = args["title"].lower()
    found = None

    def enum_cb(hwnd, _):
        nonlocal found
        if found:
            return
        if win32gui.IsWindowVisible(hwnd):
            t = win32gui.GetWindowText(hwnd)
            if title in t.lower():
                found = hwnd

    win32gui.EnumWindows(enum_cb, None)

    if not found:
        # List visible titles for error message
        titles = []
        def list_cb(hwnd, _):
            if win32gui.IsWindowVisible(hwnd):
                t = win32gui.GetWindowText(hwnd).strip()
                if t:
                    titles.append(t)
        win32gui.EnumWindows(list_cb, None)
        fail(f"no window matching '{args['title']}', visible: {', '.join(titles[:8])}")

    # Restore if minimized
    if win32gui.IsIconic(found):
        win32gui.ShowWindow(found, win32con.SW_RESTORE)
        time.sleep(0.1)

    # Focus using AttachThreadInput trick
    try:
        fg = win32gui.GetForegroundWindow()
        fg_tid = win32api.GetWindowThreadProcessId(fg)[0] if fg else 0
        target_tid = win32api.GetWindowThreadProcessId(found)[0]
        cur_tid = win32api.GetCurrentThreadId()

        if fg_tid != target_tid:
            ctypes.windll.user32.AttachThreadInput(cur_tid, target_tid, True)
            win32gui.BringWindowToTop(found)
            win32gui.SetForegroundWindow(found)
            ctypes.windll.user32.AttachThreadInput(cur_tid, target_tid, False)
        else:
            win32gui.SetForegroundWindow(found)
    except Exception:
        win32gui.SetForegroundWindow(found)

    time.sleep(0.05)
    ok({"focused": win32gui.GetWindowText(found)})


def action_close_window(args):
    """Close a window by title substring via WM_CLOSE."""
    if not HAS_WIN32:
        fail("win32gui not installed")

    title = args["title"].lower()
    found = None

    def enum_cb(hwnd, _):
        nonlocal found
        if found:
            return
        if win32gui.IsWindowVisible(hwnd):
            t = win32gui.GetWindowText(hwnd)
            if title in t.lower():
                found = hwnd

    win32gui.EnumWindows(enum_cb, None)

    if not found:
        fail(f"no window matching '{args['title']}'")

    win32gui.PostMessage(found, win32con.WM_CLOSE, 0, 0)
    time.sleep(0.1)
    ok({"closed": win32gui.GetWindowText(found)})


def action_screenshot(args):
    """Capture screenshot and save to path."""
    path = args.get("path", "screenshot.png")

    try:
        import mss
        from PIL import Image
        with mss.mss() as sct:
            monitor = sct.monitors[1]  # primary
            raw = sct.grab(monitor)
            img = Image.frombytes("RGB", raw.size, raw.bgra, "raw", "BGRX")
            img.save(path)
    except Exception:
        if HAS_PYAUTO:
            pyautogui.screenshot(path)
        else:
            fail("no screenshot backend available")

    ok({"saved": path})


# --- Main ---

ACTIONS = {
    "uia_dump": action_uia_dump,
    "click": action_click,
    "type_text": action_type_text,
    "press_keys": action_press_keys,
    "scroll": action_scroll,
    "focus_window": action_focus_window,
    "close_window": action_close_window,
    "screenshot": action_screenshot,
}

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=ACTIONS.keys())
    parser.add_argument("--args", type=str, default="{}")
    parsed = parser.parse_args()

    try:
        args = json.loads(parsed.args)
    except json.JSONDecodeError as e:
        fail(f"invalid JSON args: {e}")

    ACTIONS[parsed.action](args)


if __name__ == "__main__":
    # Import ctypes for force_focus in click action
    import ctypes
    main()
