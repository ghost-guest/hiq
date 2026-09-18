// Package assets embeds the built-in, cross-platform skill payloads that hiq
// ships with its binary. Embedding lets a single downloaded executable carry
// everything it needs, instead of distributing a separate .hiq "tail".
//
// Currently this embeds the ppt-auto skill (SVG → PPTX, pure Python + python-pptx).
// The heavy, platform-specific Python runtime is intentionally NOT embedded: the
// released payload expects the user's system Python 3.10+ and installs its pip
// dependencies on first use (see pptauto/setup_python.*). This keeps the binary
// lean and the skill genuinely cross-platform (macOS/Linux/Windows).
package assets

import "embed"

// pptauto is the embedded ppt-auto skill tree. It is released to the user's
// ~/.hiq/skills/ppt-auto/ on first run by EnsurePPTAutoSkill.
//
// The `all:` prefix is required: the tree contains entries whose names begin
// with `_` (Python __init__.py, templates/_index.md) which a bare //go:embed
// pattern silently skips, truncating the skill. all: includes them.
//
//go:embed all:pptauto
var pptauto embed.FS

// scripts holds helper scripts shared by Go-side subprocess callers — currently
// pdf_to_page_images.py (PDF → per-page PNG via PyMuPDF), used by the PPT vision
// pre-analysis (desktop/pdf_pages_vision.go) and the RAG PDF path. It used to
// sit only at the repo root, which docconv.FindScript probes relative to the
// exe — a packaged/installed binary runs from layouts where that probe misses,
// so the whole PDF→PPT visual path died with "pdf_to_page_images.py not found".
// Embedding + releasing to ~/.hiq/scripts/ (EnsureHelperScripts) makes it
// available everywhere the binary runs.
//
//go:embed scripts
var scripts embed.FS
