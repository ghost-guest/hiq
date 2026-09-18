package responses

import "github.com/zzycxz/hiq/internal/provider"

// The effort vocabulary of a relayed DeepSeek SKU is resolved in
// ReasoningForConfig below via deepSeekModelID (vendor.go), shared with the
// stateless/tool-call-reasoning wire traits a relay inherits from the family.
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
