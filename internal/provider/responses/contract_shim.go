package responses

import (
	"net/http"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// fairpeer adaptation notes (vs upstream Reasonix):
//
// Upstream consults a versioned catalog of the official "OpenCode Go" routes
// (DeepSeek-hosted endpoints) to pin reasoning protocols, replay contracts and
// static output limits (provider.ApplyOpenCodeGoContract /
// provider.LookupOfficialOpenCodeGo). fairpeer ships without that catalog, and
// for every custom endpoint the upstream lookup misses anyway — the config
// passes through unchanged. These shims preserve exactly that behavior;
// official-route polish is intentionally not carried.

// applyOpenCodeGoContract is a no-op stand-in for upstream's catalog-driven
// contract application. Custom endpoints never matched the catalog.
func applyOpenCodeGoContract(cfg provider.Config) provider.Config { return cfg }

// officialOutputLimit always misses: fairpeer has no static per-model catalog,
// so output budgets fall through to the vendor detection ladder below.
func officialOutputLimit(baseURL, model string) (int, bool) { return 0, false }

var _ = netclient.ProxySpec{} // keep the netclient import aligned with Config

// newClientIdentityHeaders builds the per-client transport identity upstream
// sends with every request. Upstream branded it "Reasonix"; fairpeer keeps its
// own identity. Only official OpenCode Go routes consume these headers.
func newClientIdentityHeaders() http.Header {
	return http.Header{"User-Agent": []string{"Fairpeer"}}
}

// applyOpenCodeGoHeaders is a no-op: it only ever set identity headers on the
// official /zen/go/v1/* routes, which fairpeer does not ship a catalog for.
func applyOpenCodeGoHeaders(_ *http.Request, _ string, _ http.Header) {}
