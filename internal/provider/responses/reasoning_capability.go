package responses

import (
	"strings"

	"github.com/zzycxz/fairpeer/internal/provider"
)

// deepSeekModelID reports whether the model ID itself names a DeepSeek SKU
// (deepseek-flash, deepseek-v4.1-flash, ...). Vendor detection is host-based,
// but the effort vocabulary is a property of the model family: relays and
// gateways fronting DeepSeek (aiaaa, one-api style hosts) accept the same
// efforts as the official endpoint even though their host is unknown to
// DetectVendor. An explicit reasoning_protocol = "none" or a declared
// supported_efforts list still wins.
func deepSeekModelID(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

func ReasoningForConfig(cfg provider.Config) provider.ReasoningCapability {
	cfg = applyOpenCodeGoContract(cfg)
	protocol, _ := cfg.Extra["reasoning_protocol"].(string)
	if protocol == "none" {
		return provider.ReasoningOptions("")
	}
	cap := provider.ReasoningOptions("")
	if protocol == "deepseek" {
		return provider.DeclaredReasoning(cfg, provider.ReasoningOptions("high", "none", "low", "high", "max"))
	}
	switch DetectVendor(cfg.BaseURL) {
	case "deepseek":
		cap = provider.ReasoningOptions("high", "none", "low", "high", "max")
	case "mimo":
		cap = provider.ReasoningOptions("", "none", "low", "medium", "high")
	default:
		switch {
		case deepSeekModelID(cfg.Model):
			// Relayed DeepSeek SKU: gateways forward (or normalize) the
			// reasoning effort, so the unified low/medium/high vocabulary
			// applies — verified live against aiaaa.cc /v1/responses with
			// both high and medium (2026-09-16). Keep the DeepSeek default
			// depth (high); "max" is not claimed for unverified relays but
			// can be declared via supported_efforts.
			cap = provider.ReasoningOptions("high", "low", "medium", "high")
		case protocol == "openai":
			cap = provider.ReasoningOptions("", "low", "medium", "high")
		}
	}
	return provider.DeclaredReasoning(cfg, cap)
}
func (c *client) ReasoningCapability() provider.ReasoningCapability { return c.reasoning.Clone() }
