package responses

import "github.com/zzycxz/hiq/internal/provider"

func (c *client) ReasoningReplayCapabilities() provider.ReasoningReplayCapabilities {
	fallback := ""
	if c.AllowsEmptyReasoningFallback() {
		fallback = "omit-item"
	}
	return provider.ReasoningReplayCapabilities{Format: "responses-items", EmptyFallback: fallback}
}
