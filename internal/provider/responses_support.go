package provider

import "strings"

// This file carries the additive provider v2 surface the Responses API
// implementation needs: output-budget defaults and the reasoning-replay
// capability contract. Everything here is append-only relative to the legacy
// hiq provider surface — existing kinds never read these, so their wire
// output is unchanged. Search policy and server-search types live in
// server_search.go.

// Auto ladder for max_output_tokens=0. Bounds completion only; never compact_ratio.
// Official DeepSeek does not use this ladder: Chat/Responses omit the field
// (server 384K ceiling) and Anthropic sends DeepSeekMaxOutputTokens.
const (
	DefaultOrdinaryOutputTokens      = 16 * 1024  // non-reasoning / non-DeepSeek
	DefaultReasoningOutputTokens     = 32 * 1024  // ordinary reasoning / MiMo
	DefaultHighReasoningOutputTokens = 64 * 1024  // high/max effort on non-DeepSeek
	DefaultHighOutputTokens          = 128 * 1024 // explicit only; never auto
	// DeepSeekMaxOutputTokens is the official V4 Flash/Pro completion ceiling.
	// Pricing page: 输出长度最大 384K. K is decimal thousands, matching the
	// documented 1M context = 1,000,000 tokens.
	DeepSeekMaxOutputTokens = 384_000
)

// AutoOutputBudget maps max_output_tokens=0 to 16K/32K/64K for non-DeepSeek
// vendors. Official DeepSeek omits the field (Chat/Responses) or sends
// DeepSeekMaxOutputTokens (Anthropic).
func AutoOutputBudget(reasoningEnabled bool, effort string) int {
	if !reasoningEnabled {
		return DefaultOrdinaryOutputTokens
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high", "max":
		return DefaultHighReasoningOutputTokens
	default:
		return DefaultReasoningOutputTokens
	}
}

// ReasoningReplayCapabilities describes the wire requirements for replaying a
// reasoning item on the next request, independently of whether a particular
// assistant turn requires replay. Unknown endpoints keep the zero value
// (legacy policy: drop reasoning, replay nothing).
type ReasoningReplayCapabilities struct {
	Format           string
	RequireSignature bool
	EmptyFallback    string
}

// ReasoningCapabilitiesProvider is implemented by providers that can describe
// their reasoning-replay wire requirements.
type ReasoningCapabilitiesProvider interface {
	ReasoningReplayCapabilities() ReasoningReplayCapabilities
}

// ReplayCapabilities returns p's reasoning-replay requirements, or the zero
// value when p is nil or does not implement the capability interface.
func ReplayCapabilities(p Provider) ReasoningReplayCapabilities {
	if p == nil {
		return ReasoningReplayCapabilities{}
	}
	if c, ok := p.(ReasoningCapabilitiesProvider); ok {
		return c.ReasoningReplayCapabilities()
	}
	return ReasoningReplayCapabilities{}
}
