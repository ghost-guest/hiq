package portable

import (
	"path"
	"strings"
)

// allowedExt is the extension whitelist for bundled files. An allow-list rather
// than a deny-list is the point: a new file type invented later is excluded by
// default instead of quietly travelling. Skills are Markdown plus assets and
// helper scripts; anything outside this set is skipped and reported, not
// silently dropped and not fatal.
var allowedExt = map[string]bool{
	// Prose and structured data.
	".md": true, ".markdown": true, ".txt": true, ".rst": true,
	".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true,
	".csv": true, ".tsv": true, ".xml": true, ".ini": true,
	// Helper code a skill's runner may execute.
	".sh": true, ".bash": true, ".ps1": true, ".bat": true, ".cmd": true,
	".py": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".rb": true, ".pl": true, ".go": true, ".sql": true,
	// Markup the panel renders.
	".html": true, ".htm": true, ".css": true,
	// Images and documents a skill may reference.
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".svg": true, ".ico": true, ".bmp": true,
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true, ".zip": true,
}

// deniedExt is checked BEFORE the whitelist. These extensions carry material
// that must never enter a bundle even if someone adds them to the whitelist
// later by mistake.
var deniedExt = map[string]bool{
	".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true,
	".keystore": true, ".age": true, ".gpg": true, ".asc": true,
}

// deniedNames are exact (case-insensitive) file or directory base names that
// never travel: the credential store, the config file, and the conventional
// secret files a user might have dropped into a skill folder.
var deniedNames = map[string]bool{
	"secrets.enc.json":  true,
	"secrets.json":      true,
	"credentials":       true,
	"credentials.json":  true,
	"config.toml":       true,
	"config.local.toml": true,
	".env":              true,
	".env.local":        true,
	"id_rsa":            true,
	"id_ed25519":        true,
	"id_ecdsa":          true,
	"known_hosts":       true,
	".netrc":            true,
	"_netrc":            true,
	".npmrc":            true,
	".pypirc":           true,
	"auth.json":         true,
	"token.json":        true,
}

// deniedDirNames are directory names that are never descended into.
var deniedDirNames = map[string]bool{
	".git": true, ".ssh": true, ".gnupg": true, ".aws": true,
	"node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	".cache": true, "sessions": true, "stats": true,
}

// isDeniedName reports whether a file or directory base name must never be
// bundled. It is deliberately case-insensitive: Windows would treat
// SECRETS.ENC.JSON and secrets.enc.json as the same file.
func isDeniedName(base string) bool {
	b := strings.ToLower(strings.TrimSpace(base))
	if b == "" {
		return false
	}
	if deniedNames[b] {
		return true
	}
	// Anything that looks like a credential or a local override, regardless of
	// its exact name: "secrets.prod.json", "config.local.toml", "id_rsa.pub".
	if strings.HasPrefix(b, "secrets") || strings.HasPrefix(b, "credentials") ||
		strings.Contains(b, ".local.") || strings.HasSuffix(b, ".local") {
		return true
	}
	if strings.HasPrefix(b, ".env") || strings.HasPrefix(b, "id_rsa") ||
		strings.HasPrefix(b, "id_ed25519") || strings.HasPrefix(b, "id_ecdsa") {
		return true
	}
	return false
}

// isDeniedDir reports whether a directory must not be descended into.
func isDeniedDir(base string) bool {
	b := strings.ToLower(strings.TrimSpace(base))
	return deniedDirNames[b] || isDeniedName(b)
}

// extAllowed reports whether a file's extension is whitelisted.
func extAllowed(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	if ext == "" {
		// Extensionless files are almost always binaries or local leftovers;
		// a skill that genuinely needs one should say so by extension.
		return false
	}
	if deniedExt[ext] {
		return false
	}
	return allowedExt[ext]
}

// scrubMode describes how an optional PII hook treats a file. Bundles carry
// text; running the scrubber over every entry would corrupt binary assets, so
// the caller decides per extension.
type scrubMode int

const (
	// scrubSkip leaves the bytes untouched (binary assets).
	scrubSkip scrubMode = iota
	// scrubText runs the optional hook (prose and structured data).
	scrubText
)

// scrubModeFor picks a mode from the extension.
func scrubModeFor(name string) scrubMode {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".txt", ".rst", ".json", ".jsonl", ".yaml", ".yml",
		".toml", ".csv", ".tsv", ".xml", ".ini", ".html", ".htm", ".css":
		return scrubText
	default:
		return scrubSkip
	}
}
