// Package openai implements the OpenAI-compatible /chat/completions provider.
// It self-registers under the "openai" kind, so any OpenAI-compatible endpoint
// (Qwen, DeepSeek, GLM, MiniMax, etc.) is just a config instance rather than
// code. All providers use the standard reasoning_effort scale (low/medium/high).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// defaultStreamIdleTimeout caps how long a started SSE stream may go without any
// bytes before it's treated as a dropped connection. A half-open TCP connection
// (e.g. a proxy switched mid-stream) sends no RST, so scanner.Scan() would block
// forever; this turns that hang into a recoverable error. Generous on purpose —
// live streams emit tokens/keepalives far more often. Stored per-client
// (client.idleTimeout) so a test can shorten it without a shared global that
// would race other streams' watchdogs.
// NOTE: Increased to 30m to accommodate deep-reasoning models (e.g. o1/R1/GLM
// thinking) that can spend a long time in the reasoning phase before emitting
// the first visible token, especially for complex tasks like PPT generation.
const defaultStreamIdleTimeout = 30 * time.Minute

func init() {
	provider.Register("openai", New)
}

// New builds an OpenAI-compatible provider from a resolved config.
func New(cfg provider.Config) (provider.Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai: base_url is required for provider %q", cfg.Name)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("openai: model is required for provider %q", cfg.Name)
	}
	name := cfg.Name
	if name == "" {
		name = "openai"
	}
	keyEnv, _ := cfg.Extra["api_key_env"].(string) // for actionable auth errors
	effort, _ := cfg.Extra["effort"].(string)
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "auto" {
		effort = ""
	}
	protocol, _ := cfg.Extra["reasoning_protocol"].(string)
	protocol = normalizeReasoningProtocol(protocol)
	switch protocol {
	case "", "openai", "none", "minimax":
		// valid — dialect resolved below
	default:
		return nil, fmt.Errorf("openai: provider %q: unknown reasoning_protocol %q (valid: auto|openai|minimax|none)", name, protocol)
	}
	// Endpoint dialect: an explicit "minimax" forces the MiniMax thinking knob
	// regardless of host (gateway/proxy BaseURLs defeat host sniffing); with
	// auto, the canonical MiniMax hosts opt in by hostname.
	minimax := protocol == "minimax" || (protocol == "" && IsMiniMax(cfg.BaseURL))
	switch {
	case protocol == "none":
		effort = ""
	case minimax:
		// MiniMax: binary adaptive/disabled knob. Map unified levels:
		// low/medium → disabled, high → adaptive
		switch effort {
		case "":
			// auto — leave empty so the wire emits thinking.type=adaptive
		case "low", "medium":
			effort = "disabled"
		case "high", "adaptive":
			effort = "adaptive"
		case "disabled":
			// pass through
		default:
			return nil, fmt.Errorf("openai: provider %q uses MiniMax thinking; effort must be low, medium, or high", name)
		}
	case effort != "":
		// Standard OpenAI-compatible: pass through low/medium/high directly.
		switch effort {
		case "low", "medium", "high":
			// pass through
		default:
			return nil, fmt.Errorf("openai: provider %q: effort must be low, medium, or high", name)
		}
	}
	httpClient, err := newHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("openai: network: %w", err)
	}
	vision, _ := cfg.Extra["vision"].(bool)
	visionDetail, _ := cfg.Extra["vision_detail"].(string)
	if visionDetail == "" {
		visionDetail = "auto"
	}
	return &client{
		name:         name,
		apiKey:       cfg.APIKey,
		keyEnv:       keyEnv,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		model:        cfg.Model,
		minimax:      minimax,
		effort:       effort,
		vision:       vision,
		visionDetail: visionDetail,
		http:         httpClient,
		idleTimeout:  defaultStreamIdleTimeout,
	}, nil
}

func newHTTPClient(cfg provider.Config) (*http.Client, error) {
	spec, _ := cfg.Extra["proxy_spec"].(netclient.ProxySpec)
	return netclient.NewHTTPClient(spec, netclient.TransportOptions{
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 180 * time.Second, // models can think for a while before the first token; increased for slow models
	})
}

type client struct {
	name         string
	apiKey       string
	keyEnv       string // api_key_env name, surfaced in auth errors
	baseURL      string
	model        string
	http         *http.Client
	minimax      bool                            // true for api.minimaxi.com — emits MiniMax-M3's thinking knob instead of reasoning_effort
	effort       string                          // reasoning_effort for OpenAI; thinking.type for MiniMax; "" = auto/provider default
	vision       bool                            // true when the provider supports image_url content parts
	visionDetail string                          // "auto", "low", "high" — forwarded as image detail level
	idleTimeout  time.Duration                   // SSE stall watchdog window; defaultStreamIdleTimeout unless a test overrides
	authed       atomic.Bool                     // true after first successful auth; enables transient 401 retry
	onReplay     func(ctx context.Context) error // optional: charged by the RPM limiter on each mid-stream replay
}

func (c *client) Name() string { return c.name }

// SetOnReplay installs a hook fired immediately before each mid-stream
// reconnect replay (the streamWithReconnect path). The RPM rate limiter sets
// it so a replay — which sends a fresh HTTP request to the gateway — draws a
// second slot from the budget, matching the real request count the provider
// meters. Without this, one user-facing Stream() call could issue up to
// maxStreamReconnects+1 gateway requests while the budget counted only one.
func (c *client) SetOnReplay(fn func(ctx context.Context) error) { c.onReplay = fn }

func normalizeReasoningProtocol(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "openai", "minimax", "none":
		return v
	default:
		// "" (auto) passes through; UNKNOWN values pass through verbatim so
		// New() rejects them with the valid-value list — silently mapping a
		// typo to auto would hide the misconfiguration from the user.
		return v
	}
}

// ModelSupportsVision reports whether a model can accept image input. Vision
// capability is declared per-provider via ProviderEntry.Vision (toml "vision"),
// not by a hardcoded model allowlist. The modelID parameter is retained for
// signature compatibility but no longer consulted.
func ModelSupportsVision(_ string, providerOverride bool) bool {
	return providerOverride
}

// bufPool reuses byte buffers for JSON-marshalled request bodies. Each turn
// allocates a buffer, marshals the request, and sends it — pooling avoids the
// GC churn from repeated alloc/free of ~10-100KB buffers. The pool is
// provider-level (not global) so OpenAI and Anthropic don't compete.
var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(c.buildRequest(req)); err != nil {
		bufPool.Put(buf)
		return nil, fmt.Errorf("%s: marshal request: %w", c.name, err)
	}
	body := make([]byte, buf.Len())
	copy(body, buf.Bytes())
	bufPool.Put(buf)

	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}
	resp, err := provider.SendWithRetry(ctx, c.http, provider.SendOptions{
		ProvName:   c.name,
		KeyEnv:     c.keyEnv,
		KeyPresent: c.apiKey != "",
		RetryAuth:  c.authed.Load(),
	}, newReq)
	if err != nil {
		return nil, err
	}
	c.authed.Store(true)

	out := make(chan provider.Chunk)
	go c.streamWithReconnect(ctx, resp, newReq, out)
	return out, nil
}

// maxStreamReconnects bounds how many times a mid-stream connection drop is
// replayed from scratch before the error is surfaced — each replay re-runs the
// whole request (cheap under prompt caching, but not free).
const maxStreamReconnects = 3

// streamWithReconnect drives readStream and, when the connection is cut before
// any model output has been forwarded, replays the request rather than failing
// the turn. Once a token (reasoning/text/tool-call) has been emitted, a replay
// would duplicate output, so the error is surfaced instead.
func (c *client) streamWithReconnect(ctx context.Context, resp *http.Response, newReq func(context.Context) (*http.Request, error), out chan<- provider.Chunk) {
	defer close(out)
	for attempt := 0; ; attempt++ {
		emitted, err := c.readStream(ctx, resp, out)
		if err == nil {
			return
		}
		if !provider.IsConnReset(err) {
			out <- provider.Chunk{Type: provider.ChunkError, Err: err}
			return
		}
		if emitted {
			out <- provider.Chunk{Type: provider.ChunkError, Err: &provider.StreamInterruptedError{Err: err}}
			return
		}
		if attempt >= maxStreamReconnects {
			out <- provider.Chunk{Type: provider.ChunkError, Err: err}
			return
		}
		// A replay is a fresh gateway request, so charge the RPM budget for it
		// (the outer Stream() already charged the first attempt). If the budget
		// is exhausted or the context is cancelled, surface that rather than
		// silently bypassing the limit. onReplay is nil when no limiter wraps
		// this client (e.g. tests, or RPM disabled) — then replays are free.
		if c.onReplay != nil {
			if rerr := c.onReplay(ctx); rerr != nil {
				out <- provider.Chunk{Type: provider.ChunkError, Err: rerr}
				return
			}
		}
		next, rerr := provider.SendWithRetry(ctx, c.http, provider.SendOptions{
			ProvName:   c.name,
			KeyEnv:     c.keyEnv,
			KeyPresent: c.apiKey != "",
			RetryAuth:  c.authed.Load(),
		}, newReq)
		if rerr != nil {
			out <- provider.Chunk{Type: provider.ChunkError, Err: rerr}
			return
		}
		resp = next
	}
}

func (c *client) buildRequest(req provider.Request) chatRequest {
	// Repair tool-call pairing before sending: an interrupted/resumed history can
	// carry an assistant tool_calls turn whose results never landed, which the
	// API rejects with a 400 ("must be followed by tool messages …").
	src := provider.SanitizeToolPairing(req.Messages)
	msgs := make([]chatMessage, len(src))
	for i, m := range src {
		cm := chatMessage{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			wire := chatToolCall{ID: tc.ID, Type: "function"}
			wire.Function.Name = tc.Name
			wire.Function.Arguments = tc.Arguments
			cm.ToolCalls = append(cm.ToolCalls, wire)
		}
		if m.Role != provider.RoleAssistant || len(cm.ToolCalls) == 0 || m.Content != "" {
			cm.Content = m.Content
			// For vision-capable models, convert image references to the
			// multimodal wire format with detail level. Audio (input_audio)
			// blocks convert unconditionally — they target models the user
			// explicitly chose as voice_model, which are audio-capable by
			// definition; there's no separate "audio" capability flag to gate on.
			if m.Role == provider.RoleUser {
				if (ModelSupportsVision(c.model, c.vision) && len(m.Images) > 0) || len(m.Audio) > 0 {
					cm.Content = imageContentParts(m.Content, m.Images, m.Audio, c.visionDetail)
				}
			}
		}
		msgs[i] = cm
	}

	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type:     "function",
			Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}

	out := chatRequest{
		Model:         c.model,
		Messages:      msgs,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
	}
	// Cache-affinity routing (upgrade spec 4-6): same conversation → same
	// shard. The API caps the key at 64 chars; truncate by runes to stay valid.
	if req.CacheKey != "" {
		out.PromptCacheKey = truncateRunes(req.CacheKey, 64)
	}
	// Constrained JSON output (upgrade spec 4-7) when the caller asked for it.
	if len(req.ResponseSchema) > 0 {
		name := req.SchemaName
		if name == "" {
			name = "result"
		}
		out.ResponseFormat = &chatResponseFormat{
			Type:       "json_schema",
			JSONSchema: chatJSONSchemaFmt{Name: name, Schema: req.ResponseSchema},
		}
	}
	switch {
	case c.minimax:
		// M3 uses a single `thinking.type` field with two valid values:
		// "adaptive" (default, thinking on) and "disabled" (off). Reasoning
		// depth is not a knob on M3, so reasoning_effort is omitted entirely.
		t := c.effort
		if t == "" {
			t = "adaptive" // /effort auto == the M3 model default
		}
		out.Thinking = &thinkingMode{Type: t}
	default:
		// OpenAI-compatible: use standard reasoning_effort field.
		out.ReasoningEffort = c.effort
	}
	return out
}

// readStream parses one SSE response into chunks: text deltas stream live,
// tool-call fragments accumulate by index and emit complete on [DONE], and a
// ChunkToolCallStart fires the moment a call's name is known. It returns whether
// any model output was forwarded (so the caller can decide a replay is safe) and
// the first fatal error — a nil error means the stream reached [DONE].
func (c *client) readStream(ctx context.Context, resp *http.Response, out chan<- provider.Chunk) (emitted bool, _ error) {
	defer resp.Body.Close()

	// Close the response body when the context is canceled (user interrupt) or the
	// stream stalls past c.idleTimeout, so scanner.Scan() unblocks instead of
	// hanging on a half-open connection. done lets the watchdog exit on a normal
	// return — otherwise it outlives the call and blocks forever on a non-cancellable
	// context whose Done() is nil. The watchdog owns the timer; the read loop only
	// pings the buffered activity channel, so there's no Timer.Reset race.
	idleTimeout := c.idleTimeout
	if idleTimeout <= 0 { // zero-value client (constructed without New)
		idleTimeout = defaultStreamIdleTimeout
	}
	done := make(chan struct{})
	defer close(done)
	activity := make(chan struct{}, 1)
	var stalled atomic.Bool
	go func() {
		idle := time.NewTimer(idleTimeout)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				resp.Body.Close()
				return
			case <-idle.C:
				stalled.Store(true)
				resp.Body.Close()
				return
			case <-activity:
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(idleTimeout)
			case <-done:
				return
			}
		}
	}()

	acc := map[int]*provider.ToolCall{}
	started := map[int]bool{}
	var order []int
	var lastFinishReason string
	var sawDone bool
	var think thinkSplitter

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select { // ping the idle watchdog; non-blocking so a full buffer is fine
		case activity <- struct{}{}:
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			break
		}

		var sr streamResponse
		if err := json.Unmarshal([]byte(data), &sr); err != nil {
			return emitted, fmt.Errorf("%s: decode stream: %w", c.name, err)
		}
		if sr.Error != nil {
			return emitted, fmt.Errorf("%s: %s", c.name, sr.Error.Message)
		}
		if len(sr.Choices) > 0 && sr.Choices[0].FinishReason != nil && *sr.Choices[0].FinishReason != "" {
			lastFinishReason = *sr.Choices[0].FinishReason
		}
		if sr.Usage != nil {
			u := normaliseUsage(sr.Usage)
			u.FinishReason = lastFinishReason
			emitted = true
			out <- provider.Chunk{Type: provider.ChunkUsage, Usage: u}
		}
		if len(sr.Choices) == 0 {
			continue
		}

		delta := sr.Choices[0].Delta
		rc := delta.ReasoningContent
		if rc == "" {
			rc = delta.Reasoning
		}
		if rc != "" {
			emitted = true
			out <- provider.Chunk{Type: provider.ChunkReasoning, Text: rc}
		}
		if delta.Content != "" {
			r, txt := think.push(delta.Content)
			if r != "" {
				emitted = true
				out <- provider.Chunk{Type: provider.ChunkReasoning, Text: r}
			}
			if txt != "" {
				emitted = true
				out <- provider.Chunk{Type: provider.ChunkText, Text: txt}
			}
		}
		for _, tc := range delta.ToolCalls {
			cur, ok := acc[tc.Index]
			if !ok {
				cur = &provider.ToolCall{}
				acc[tc.Index] = cur
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Name = tc.Function.Name
			}
			cur.Arguments += tc.Function.Arguments
			// Signal the call's start the moment its name is known, so a frontend
			// can show the tool card immediately rather than only after its
			// (possibly large) arguments finish streaming.
			if !started[tc.Index] && cur.Name != "" {
				started[tc.Index] = true
				emitted = true
				out <- provider.Chunk{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: cur.ID, Name: cur.Name}}
			}
			// Forward the raw fragment (upgrade spec 3-3): the agent layer turns
			// apply_patch deltas into a live diff preview. Cheap — one channel
			// send per SSE tool fragment, which is already flowing.
			if tc.Function.Arguments != "" {
				out <- provider.Chunk{Type: provider.ChunkToolArgsDelta, Text: tc.Function.Arguments, ToolCall: &provider.ToolCall{ID: cur.ID, Name: cur.Name}}
			}
		}
	}

	if stalled.Load() {
		return emitted, fmt.Errorf("%s: stream stalled — no data for %s, connection likely dropped", c.name, idleTimeout)
	}
	if err := scanner.Err(); err != nil {
		return emitted, fmt.Errorf("%s: read stream: %w", c.name, err)
	}
	// A proxy that idle-closes with a clean FIN ends the scan with no error. Without
	// this check the turn would be committed as complete — including half-streamed
	// tool-call arguments, which then 400 on every replay (#3953).
	if !sawDone && lastFinishReason == "" {
		return emitted, fmt.Errorf("%s: stream ended before completion: %w", c.name, io.ErrUnexpectedEOF)
	}

	if r, txt := think.flush(); r != "" || txt != "" {
		if r != "" {
			out <- provider.Chunk{Type: provider.ChunkReasoning, Text: r}
		}
		if txt != "" {
			out <- provider.Chunk{Type: provider.ChunkText, Text: txt}
		}
	}

	sort.Ints(order)
	for _, idx := range order {
		tc := acc[idx]
		if tc.ID == "" {
			// Some OpenAI-compatible gateways stream tool calls by index with no id.
			// Synthesize a stable one so the result can be paired back to its call —
			// an empty tool_call_id collapses multi-tool turns downstream.
			tc.ID = fmt.Sprintf("call_%d", idx)
		}
		out <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: tc}
	}
	out <- provider.Chunk{Type: provider.ChunkDone}
	return emitted, nil
}

// normaliseUsage folds the two cache-hit shapes the OpenAI-compatible ecosystem
// uses into a single Usage: some providers put prompt_cache_{hit,miss}_tokens at
// the top of usage; others put it nested under prompt_tokens_details. Whichever
// side reports non-zero wins; miss is derived when only hit is given.
// Reasoning tokens land in completion_tokens_details on thinking-mode models.
// Note: some providers do not report cache tokens (both fields are 0); the
// normalisation logic is kept for future cache support and for other providers.
func normaliseUsage(u *wireUsage) *provider.Usage {
	hit := u.PromptCacheHitTokens
	miss := u.PromptCacheMissTokens
	if hit == 0 && u.PromptTokensDetails != nil {
		hit = u.PromptTokensDetails.CachedTokens
	}
	if miss == 0 && hit > 0 && u.PromptTokens > hit {
		miss = u.PromptTokens - hit
	}
	reasoning := 0
	if u.CompletionTokensDetails != nil {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	return &provider.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   hit,
		CacheMissTokens:  miss,
		ReasoningTokens:  reasoning,
	}
}

// --- OpenAI-compatible wire protocol ---

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []chatMessage  `json:"messages"`
	Tools           []chatTool     `json:"tools,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
	Temperature     float64        `json:"temperature,omitempty"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"` // OpenAI standard
	Thinking        *thinkingMode  `json:"thinking,omitempty"`
	// PromptCacheKey routes same-conversation requests to one cache shard
	// (OpenAI prefix caching). Clamped to the API's 64-char limit upstream.
	PromptCacheKey  string `json:"prompt_cache_key,omitempty"`
	// ResponseFormat constrains output to the given JSON schema (upgrade spec
	// 4-7). Only set when the caller passed one.
	ResponseFormat  *chatResponseFormat `json:"response_format,omitempty"`
}

type chatResponseFormat struct {
	Type       string           `json:"type"` // "json_schema"
	JSONSchema chatJSONSchemaFmt `json:"json_schema"`
}

type chatJSONSchemaFmt struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type thinkingMode struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role string `json:"role"`
	// content is always present (never omitted): some providers' strict
	// deserializers reject a message missing the field. A pure tool_calls
	// assistant turn serializes as null (OpenAI-spec, and what strict clones
	// expect); every other role/message serializes as a string, empty
	// included — null is rejected by some backends for a tool message.
	// For multimodal messages, content is an array of ContentPart objects.
	Content          any            `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			Reasoning        string         `json:"reasoning"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// wireUsage covers both top-level cache fields and nested details —
// normaliseUsage chooses whichever side reports values.
type wireUsage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// chatContentPart is the wire format for a content part in the OpenAI API.
type chatContentPart struct {
	Type       string              `json:"type"`
	Text       string              `json:"text,omitempty"`
	ImageURL   *chatImageURLPart   `json:"image_url,omitempty"`
	InputAudio *chatInputAudioPart `json:"input_audio,omitempty"`
	FileID     string              `json:"file_id,omitempty"`
}

// chatImageURLPart is the wire format for an image_url content part.
type chatImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// chatInputAudioPart is the wire format for an input_audio content part
// (GPT-4o-audio / MiMo-V2.5-ASR style). Data is pure base64, Format is the bare
// container ("wav", "mp3").
type chatInputAudioPart struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

// imageContentParts converts a message's text + vision references + inline
// audio into the OpenAI multimodal wire format, applying the vision detail
// level to every image. Text-only turns never reach here: buildRequest keeps
// them as a plain string so the serialized bytes stay stable for prompt
// caching. Audio parts are fairpeer-specific (upstream Reasonix has no STT
// path) and route through the same converter as input_audio blocks.
func imageContentParts(text string, images []string, audio []provider.InputAudio, detail string) []chatContentPart {
	out := make([]chatContentPart, 0, 1+len(images)+len(audio))
	if text != "" {
		out = append(out, chatContentPart{Type: "text", Text: text})
	}
	for _, ref := range images {
		switch provider.ClassifyImage(ref) {
		case provider.ImageFileID:
			out = append(out, chatContentPart{Type: "file", FileID: ref})
		case provider.ImageDataURL, provider.ImageHTTPURL:
			out = append(out, chatContentPart{
				Type:     "image_url",
				ImageURL: &chatImageURLPart{URL: ref, Detail: detail},
			})
		}
	}
	for _, a := range audio {
		if a.Data == "" {
			continue
		}
		out = append(out, chatContentPart{
			Type:       "input_audio",
			InputAudio: &chatInputAudioPart{Data: a.Data, Format: a.Format},
		})
	}
	return out
}

// imageUnderstandPrompt and the in-conversation legacy image-degradation path were
// removed (WP-2.6). Public-network users now use each vendor's native vision
// capability instead of the a vision-model fallback.

// truncateRunes cuts s to at most n runes (no mid-rune split).
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
