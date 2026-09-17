package builtin

import (
	"net/url"
	"os"
	"strings"
)

// GitHub mirror fallback (gh-accel integration).
//
// On machines behind the GFW, github.com may resolve through a local
// accelerator (watt/Steam++ hosts-hijack) while raw.githubusercontent.com and
// friends remain unreachable — measured live: api.github.com 200 in ~1.4s,
// raw.githubusercontent.com timeout. Instead of depending on any one mirror
// service, web_fetch rewrites failed GitHub fetches through a chain:
//
//	raw file URLs      -> cdn.jsdelivr.net/gh (global CDN, CN-reachable)
//	other GitHub URLs  -> prefix mirrors (gh-proxy style), env-tunable
//
// via FAIRPEER_GH_MIRRORS (comma-separated base URLs, appended before the
// original URL). Nothing here is load-bearing: mirrors are only tried after
// the direct fetch already failed.

// ghMirrorEnv overrides the prefix-mirror list without a rebuild.
const ghMirrorEnv = "FAIRPEER_GH_MIRRORS"

// defaultGhPrefixMirrors are gh-proxy style services: <mirror><original-url>.
var defaultGhPrefixMirrors = []string{"https://gh-proxy.com/", "https://ghfast.top/"}

// isGitHubHost reports whether host belongs to the GitHub content family.
func isGitHubHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range []string{
		"github.com", "raw.githubusercontent.com", "gist.githubusercontent.com",
		"gist.github.com", "codeload.github.com", "objects.githubusercontent.com",
	} {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// ghMirrorURLs returns mirror candidates for a failed GitHub fetch, best
// first. Empty when the URL isn't GitHub-hosted.
func ghMirrorURLs(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil || !isGitHubHost(u.Hostname()) {
		return nil
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")

	// raw.githubusercontent.com/<owner>/<repo>/<ref>/<path...> and
	// github.com/<owner>/<repo>/{raw,blob}/<ref>/<path...> are plain file
	// content — jsDelivr serves exactly that from a CN-friendly CDN:
	// https://cdn.jsdelivr.net/gh/<owner>/<repo>@<ref>/<path>
	if u.Hostname() == "raw.githubusercontent.com" && len(seg) >= 5 {
		return []string{"https://cdn.jsdelivr.net/gh/" + seg[0] + "/" + seg[1] + "@" + seg[2] + "/" + strings.Join(seg[3:], "/")}
	}
	if u.Hostname() == "github.com" && len(seg) >= 5 && (seg[2] == "raw" || seg[2] == "blob") {
		return []string{"https://cdn.jsdelivr.net/gh/" + seg[0] + "/" + seg[1] + "@" + seg[3] + "/" + strings.Join(seg[4:], "/")}
	}

	// Everything else (repo pages, gists, archives, release assets): prefix
	// mirrors, user-tunable via FAIRPEER_GH_MIRRORS.
	var mirrors []string
	if v := strings.TrimSpace(os.Getenv(ghMirrorEnv)); v != "" {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				mirrors = append(mirrors, strings.TrimRight(m, "/")+"/")
			}
		}
	} else {
		mirrors = defaultGhPrefixMirrors
	}
	out := make([]string, 0, len(mirrors))
	for _, m := range mirrors {
		out = append(out, m+rawURL)
	}
	return out
}

// ghFallbackWorthy reports whether a fetch outcome should trigger the mirror
// chain: transport errors always, and HTTP statuses a mirror could plausibly
// fix (rate-limit pages, origin overload). 404 is a real "not found" — a
// mirror would 404 too, so we don't retry it.
func ghFallbackWorthy(err error, status int) bool {
	if err != nil {
		return true
	}
	return status == 403 || status == 429 || status >= 500
}
