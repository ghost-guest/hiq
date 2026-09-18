"""
UIA element dump via pywinauto. Called by hiq's dumpUIA() via subprocess.
Outputs JSON array of elements to stdout.

Usage: python scripts/uia_dump.py [--screen-width W --screen-height H]
"""
import sys
import io
import json
import argparse

# Force UTF-8 stdout (Windows defaults to GBK which breaks on Unicode names)
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

try:
    from pywinauto import Desktop
except ImportError:
    print(json.dumps({"error": "pywinauto not installed"}))
    sys.exit(1)


def dump_elements(screen_w: int, screen_h: int) -> list:
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

                    # Skip elements outside screen bounds
                    if cx < 0 or cy < 0 or cx > screen_w or cy > screen_h:
                        continue

                    # 15px grid dedup (smaller area wins)
                    grid_key = f"{cx // 15}_{cy // 15}"
                    area = (x2 - x1) * (y2 - y1)
                    # Mark container types (matches Rooster engine.py:335-347)
                    is_container = c_type in {
                        "Pane", "Group", "List", "Tab", "ToolBar",
                        "Window", "Custom", "Document", "Table", "Tree", "MenuBar",
                    }
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

    # Strip _area before output
    result = []
    for node in seen.values():
        node.pop("_area", None)
        result.append(node)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--screen-width", type=int, default=1920)
    parser.add_argument("--screen-height", type=int, default=1080)
    args = parser.parse_args()

    try:
        result = dump_elements(args.screen_width, args.screen_height)
        print(json.dumps(result, ensure_ascii=False))
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
