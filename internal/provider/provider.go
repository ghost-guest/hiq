// Package provider defines the model-backend abstraction and a registry mapping
// a provider "kind" to a factory. Concrete implementations live in subpackages
// (e.g. provider/openai) and self-register via init(). The core resolves
// providers by kind from config and never hardcodes a specific model.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zzycxz/fairpeer/internal/nilutil"
)

// Role is the role of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single conversation message.
//
// Content is the provider-visible text. Multimodal payloads live in dedicated
// fields — Images for vision references, Audio for speech-to-text input — so a
// text-only turn serializes its content as a plain string and the
// provider-visible wire bytes stay byte-stable for prompt caching. This matches
// the upstream Reasonix contract (provider.Message.Content is a string there
// too), so packages ported from upstream compile against it unchanged.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	// Images holds vision references for this turn: inline data URLs
	// ("data:image/png;base64,..."), http(s) image URLs, or provider file ids.
	// Only embedded for vision-capable models.
	Images []string `json:"images,omitempty"`
	// Audio holds inline audio blocks for audio-capable models. This is
	// fairpeer-specific: upstream Reasonix has no speech-to-text path.
	Audio            []InputAudio `json:"audio,omitempty"`
	ReasoningContent string       `json:"reasoning_content,omitempty"` // assistant: thinking-mode chain-of-thought, round-tripped on multi-turn
	// ReasoningSignature is an opaque, provider-issued proof that ReasoningContent
	// is genuine model output. Anthropic requires the signed thinking block be
	// replayed on the next turn when a tool call followed thinking; providers
	// without signed reasoning (e.g. the openai-compatible ones) leave it empty.
	// Round-tripped alongside ReasoningContent.
	ReasoningSignature string     `json:"reasoning_signature,omitempty"`
	ToolCalls          []ToolCall `json:"tool_calls,omitempty"`   // set by assistant
	ToolCallID         string     `json:"tool_call_id,omitempty"` // links a tool result to its call
	Name               string     `json:"name,omitempty"`         // tool message: tool name
	// Original is the user prompt as originally typed, kept when the turn was
	// edited inline. Local UI metadata; provider requests ignore it.
	Original string `json:"original,omitempty"`
	// MemoryCitations is local display metadata for memories that influenced an
	// assistant turn. Provider implementations must not forward it to model APIs.
	MemoryCitations []MemoryCitation `json:"memoryCitations,omitempty"`
	// ToolRunState is the host-local run state of a tool result, used to
	// reconstruct interrupted turns. Provider implementations must not forward
	// it to model APIs.
	ToolRunState ToolRunState `json:"tool_run_state,omitempty"`
}

// UnmarshalJSON restores a message from its on-disk JSON. Current files store
// content as a plain string plus optional images/audio arrays, so the common
// path is a straight decode. Files written by fairpeer ≤ v0.1 stored content as
// a []ContentPart array instead (text + image_url + input_audio parts); those
// are flattened into Content/Images/Audio so old sessions keep loading
// losslessly rather than silently dropping their image data.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message
	var raw struct {
		alias
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Message(raw.alias)
	trimmed := bytes.TrimSpace(raw.Content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		m.Content = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		m.Content = s
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return err
	}
	m.setLegacyMultimodal(parts)
	return nil
}

// setLegacyMultimodal flattens a legacy []ContentPart content value into the
// Content/Images/Audio fields. Text parts concatenate; image and audio parts
// move to their dedicated fields. A message that carried only non-text parts
// keeps an empty Content, with the payload living in Images/Audio.
func (m *Message) setLegacyMultimodal(parts []ContentPart) {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" {
				m.Images = append(m.Images, p.ImageURL.URL)
			}
		case "input_audio":
			if p.InputAudio != nil && p.InputAudio.Data != "" {
				m.Audio = append(m.Audio, *p.InputAudio)
			}
		}
	}
	m.Content = b.String()
}

// ContentPart is one block in a multimodal message (OpenAI content parts format).
type ContentPart struct {
	Type       string      `json:"type"`                  // "text", "image_url", or "input_audio"
	Text       string      `json:"text,omitempty"`        // for Type == "text"
	ImageURL   *ImageURL   `json:"image_url,omitempty"`   // for Type == "image_url"
	InputAudio *InputAudio `json:"input_audio,omitempty"` // for Type == "input_audio"
}

// ImageURL holds a base64 data URL for inline image content.
type ImageURL struct {
	URL    string `json:"url"`              // "data:image/png;base64,..."
	Detail string `json:"detail,omitempty"` // "low", "high", "auto"
}

// InputAudio holds an inline audio block for multimodal models that accept
// audio input (OpenAI GPT-4o-audio style: {"type":"input_audio","input_audio":
// {"data":"<base64>","format":"wav"}}). Unlike ImageURL.URL (a full data URL),
// Data is the PURE base64 payload with no "data:...;base64," prefix; Format is
// the bare container ("wav", "mp3").
type InputAudio struct {
	Data   string `json:"data"`             // pure base64 (no data: prefix)
	Format string `json:"format,omitempty"` // "wav", "mp3"
}

// ContentString extracts the text portion of a Message.Content field.
// When Content is nil or a plain string, returns it directly.
// When Content is a structured multimodal block, extracts and concatenates text parts.
func ContentString(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []ContentPart:
		var b strings.Builder
		for _, p := range v {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	default:
		return fmt.Sprintf("%v", content)
	}
}

// ContentLen estimates the byte size of Content for token budgeting.
// For multimodal content, text bytes + base64 image bytes (approximate).
func ContentLen(content any) int {
	if content == nil {
		return 0
	}
	switch v := content.(type) {
	case string:
		return len(v)
	case []ContentPart:
		n := 0
		for _, p := range v {
			switch p.Type {
			case "text":
				n += len(p.Text)
			case "image_url":
				if p.ImageURL != nil {
					n += len(p.ImageURL.URL)
				}
			case "input_audio":
				if p.InputAudio != nil {
					n += len(p.InputAudio.Data)
				}
			}
		}
		return n
	default:
		return len(fmt.Sprintf("%v", content))
	}
}

// ImageMessage builds a message carrying text plus vision references
// (inline data URLs or http(s) image URLs). Only vision-capable models receive
// the images on the wire; adapters omit them otherwise.
func ImageMessage(role Role, text string, imageURLs ...string) Message {
	return Message{Role: role, Content: text, Images: append([]string(nil), imageURLs...)}
}

// AudioMessage builds a message carrying text plus inline audio. Each
// audioDataURL must be a "data:audio/wav;base64,..." URL: the prefix is split
// into a pure-base64 Data plus a bare Format so the OpenAI input_audio block is
// shaped correctly. Used by STT (CallSTT) — the model receives the audio plus a
// prompt asking it to transcribe, and returns text. Non-parseable data URLs are
// silently skipped (defensive; the frontend recorder always emits valid ones).
func AudioMessage(role Role, text string, audioDataURLs ...string) Message {
	m := Message{Role: role, Content: text}
	for _, url := range audioDataURLs {
		mediaType, b64, ok := ParseImageDataURL(url) // ParseImageDataURL is a generic data-URL splitter
		if !ok {
			continue
		}
		m.Audio = append(m.Audio, InputAudio{Data: b64, Format: audioFormatFromMime(mediaType)})
	}
	return m
}

// HasImages reports whether the message carries vision references.
func (m Message) HasImages() bool { return len(m.Images) > 0 }

// HasAudio reports whether the message carries inline audio blocks.
func (m Message) HasAudio() bool { return len(m.Audio) > 0 }

// MessageFromInput builds the user message for a turn from fairpeer's turn
// input carrier, which is either plain text or a []ContentPart multimodal
// payload (text + image_url + input_audio parts) assembled by the control
// layer. It is the single conversion point between the turn plumbing, which
// stays untyped so custom frontends can pass richer payloads, and the
// provider-visible Message contract, which keeps text in Content and moves
// non-text payloads to Images/Audio.
func MessageFromInput(input any) Message {
	switch v := input.(type) {
	case nil:
		return Message{Role: RoleUser}
	case string:
		return Message{Role: RoleUser, Content: v}
	case []ContentPart:
		m := Message{Role: RoleUser}
		m.setLegacyMultimodal(v)
		return m
	default:
		return Message{Role: RoleUser, Content: fmt.Sprintf("%v", input)}
	}
}

// audioFormatFromMime maps an audio MIME type to the bare container name used
// by the OpenAI input_audio.format field. Unknown MIME → "" (let the server
// sniff from bytes).
func audioFormatFromMime(mime string) string {
	switch strings.ToLower(mime) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	default:
		return ""
	}
}

// ParseImageDataURL splits a "data:image/png;base64,AAAA..." data URL into its
// MIME type and raw base64 payload. Returns ("", "", false) when the prefix is
// not a valid data URL.
func ParseImageDataURL(dataURL string) (mediaType, base64Data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return "", "", false
	}
	rest := dataURL[len(prefix):]
	idx := strings.Index(rest, ";base64,")
	if idx < 0 {
		return "", "", false
	}
	mediaType = rest[:idx]
	base64Data = rest[idx+len(";base64,"):]
	if mediaType == "" || base64Data == "" {
		return "", "", false
	}
	return mediaType, base64Data, true
}

// MemoryCitation is local display metadata for memories that influenced an
// assistant turn. Provider implementations must never forward it to model APIs.
type MemoryCitation struct {
	ID        string `json:"id,omitempty"`
	Source    string `json:"source"`
	LineStart int    `json:"lineStart,omitempty"`
	LineEnd   int    `json:"lineEnd,omitempty"`
	Note      string `json:"note,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// ToolCall is a tool invocation requested by the model. Arguments is raw JSON.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// Diff/Added/Removed are local display metadata describing what a write or
	// edit tool changed. They are persisted for replay/cards and deliberately
	// never serialized on the wire, so they cannot perturb the prompt-cache
	// prefix. Provider request builders must serialize only ID/Name/Arguments.
	Diff    string `json:"diff,omitempty"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
}

// ToolSchema is a tool definition exposed to the model. Parameters is JSON Schema.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Deferred marks a tool whose full schema is withheld from the first
	// request and discovered on demand (tool search). Strict opts the schema
	// into provider-side structured-output enforcement. Namespace groups
	// dynamically-namespaced tools. All three are additive over the original
	// wire shape and default to zero values.
	Deferred  bool   `json:"deferred,omitempty"`
	Strict    bool   `json:"strict,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// Request is a single completion request.
type Request struct {
	Messages    []Message
	Tools       []ToolSchema
	Temperature float64
	MaxTokens   int
	// CacheKey identifies the conversation for providers with explicit
	// cache-affinity routing (OpenAI prompt_cache_key): same key → same cache
	// shard, so a session's turns keep hitting each other's prefix cache even
	// across process restarts. Empty = omit the field (provider default).
	CacheKey string
	// ResponseSchema (upgrade spec 4-7) requests constrained JSON output from
	// providers that support it (OpenAI response_format/json_schema). Callers
	// must tolerate a provider ignoring or rejecting it — keep a free-text
	// fallback path. Providers without structured-output support simply never
	// read the field.
	ResponseSchema json.RawMessage
	// SchemaName labels the schema in the wire format (required by OpenAI's
	// json_schema response format; defaults to "result").
	SchemaName string
}

// interruptedToolResult stands in for a tool result that never landed — an
// assistant tool_calls turn whose execution was cut short (interrupt, crash) and
// later resumed. Sending such a turn unanswered trips the OpenAI 400
// "An assistant message with 'tool_calls' must be followed by tool messages
// responding to each 'tool_call_id'".
const interruptedToolResult = "[no result: the previous turn was interrupted before this tool call completed]"

// SanitizeToolPairing repairs a history for a provider request (every assistant
// tool_calls answered by a following tool message, orphan tool messages dropped,
// empty tool-call names backfilled from results, truncated args closed) right
// before sending it to the wire — without touching the stored session. Kept as a
// distinct name so call sites read as "defensive wire prep" rather than "session
// mutation". Now a thin alias over NormalizeMessages so the wire path and the
// session-load path share one repair implementation.
func SanitizeToolPairing(msgs []Message) []Message { return NormalizeMessages(msgs) }

// repairToolCallArgs returns m with any undecodable tool-call Arguments closed
// into valid JSON (copy-on-write; the caller's history is never mutated). Empty
// arguments pass through — some gateways send "" for no-arg tools.
func repairToolCallArgs(m Message) Message {
	broken := false
	for _, tc := range m.ToolCalls {
		if tc.Arguments != "" && !json.Valid([]byte(tc.Arguments)) {
			broken = true
			break
		}
	}
	if !broken {
		return m
	}
	calls := make([]ToolCall, len(m.ToolCalls))
	copy(calls, m.ToolCalls)
	for i := range calls {
		if calls[i].Arguments == "" || json.Valid([]byte(calls[i].Arguments)) {
			continue
		}
		calls[i].Arguments = closeTruncatedJSON(calls[i].Arguments)
	}
	m.ToolCalls = calls
	return m
}

// closeTruncatedJSON best-effort completes a JSON document cut off mid-stream
// (unterminated string, open braces, dangling comma/colon); anything still
// invalid after closing degrades to "{}".
func closeTruncatedJSON(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	out := s
	if esc {
		out = out[:len(out)-1]
	}
	if inStr {
		out += `"`
	}
	trimmed := strings.TrimRight(out, " \t\r\n")
	switch {
	case strings.HasSuffix(trimmed, ","):
		out = trimmed[:len(trimmed)-1]
	case strings.HasSuffix(trimmed, ":"):
		out = trimmed + "null"
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out += string(stack[i])
	}
	if !json.Valid([]byte(out)) {
		return "{}"
	}
	return out
}

// pairToolResults answers each tool_call with its result, backfilling a
// placeholder for any unanswered one. Distinct non-empty ids pair by id (so
// reordered results re-sort to call order); empty or duplicate ids pair by
// position instead — some gateways stream tool calls by index with no id, and a
// map keyed on id would collapse those results into one (call order is preserved
// because the loop appends results in call order).
func pairToolResults(calls []ToolCall, avail []Message) []Message {
	out := make([]Message, 0, len(calls))
	if idDistinct(calls) {
		byID := make(map[string]Message, len(avail))
		for _, r := range avail {
			byID[r.ToolCallID] = r
		}
		for _, tc := range calls {
			if r, ok := byID[tc.ID]; ok {
				out = append(out, r)
			} else {
				out = append(out, Message{Role: RoleTool, ToolCallID: tc.ID, Name: tc.Name, Content: interruptedToolResult})
			}
		}
		return out
	}
	for k, tc := range calls {
		if k < len(avail) {
			r := avail[k]
			r.ToolCallID = tc.ID
			out = append(out, r)
		} else {
			out = append(out, Message{Role: RoleTool, ToolCallID: tc.ID, Name: tc.Name, Content: interruptedToolResult})
		}
	}
	return out
}

// idDistinct reports whether every call carries a non-empty id unique within the
// batch — the condition under which id-keyed pairing is safe.
func idDistinct(calls []ToolCall) bool {
	seen := make(map[string]struct{}, len(calls))
	for _, tc := range calls {
		if tc.ID == "" {
			return false
		}
		if _, dup := seen[tc.ID]; dup {
			return false
		}
		seen[tc.ID] = struct{}{}
	}
	return true
}

// ChunkType identifies the kind of a streamed increment.
type ChunkType int

const (
	ChunkText          ChunkType = iota // text delta
	ChunkReasoning                      // thinking-mode reasoning delta (before the visible answer)
	ChunkToolCallStart                  // a tool call has begun (ToolCall: ID+Name; args still streaming)
	ChunkToolArgsDelta                  // tool-call argument fragment (Text: raw args delta; ToolCall: ID+Name) — for live patch previews
	ChunkToolCall                       // one complete tool call
	ChunkUsage                          // token usage for the completion
	ChunkDone                           // completion finished normally
	ChunkError                          // an error occurred
)

// Usage reports token accounting for a completion. Cache hit/miss come from
// either top-level prompt_cache_{hit,miss}_tokens or the OpenAI standard
// prompt_tokens_details.cached_tokens — the openai provider normalises
// both shapes into these fields. Note: some providers do not report cache
// tokens (both fields stay 0); the normalisation is kept for future support.
// ReasoningTokens is the thinking-mode subset of
// CompletionTokens reported by thinking-capable models. FinishReason carries
// the model's last reported choices[0].finish_reason so the agent can surface
// abnormal terminations ("length", "content_filter", "repetition_truncation").
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheHitTokens   int // prompt tokens served from cache
	CacheMissTokens  int // pure uncached prompt tokens (not cached, not a cache write)
	CacheWriteTokens int // cache-creation writes (Anthropic cache_creation_input_tokens; billed above input)
	// CacheWriteBilledTokens expresses those cache-creation writes in ordinary
	// input-token equivalents, so cost math can replace the raw write count with
	// the provider's real multiplier (Anthropic 5-minute writes bill at 1.25×
	// input). Zero falls back to 1× for providers that do not report it.
	CacheWriteBilledTokens float64
	ReasoningTokens        int    // subset of CompletionTokens spent on chain-of-thought
	FinishReason           string // "stop", "tool_calls", "length", "content_filter", "repetition_truncation", …
	// Estimated marks usage the host derived rather than read from the provider.
	Estimated bool
	// RequestCount is the number of provider requests represented by this
	// aggregate. Zero means one request, for backward compatibility; recovery
	// paths that merge multiple attempts set the exact count.
	RequestCount int
	// Unknown is set when at least one request in the aggregate reported no
	// provider usage at all.
	Unknown bool
}

// Pricing is a provider's per-1M-token rates, used to estimate spend. Currency
// is just a display symbol (default "¥"). toml tags let config decode it.
type Pricing struct {
	CacheHit   float64 `toml:"cache_hit"`   // per 1M cached prompt tokens
	CacheWrite float64 `toml:"cache_write"` // per 1M cache-creation tokens (Anthropic 5m write = 1.25× input; 0 → defaults to 1.25× Input in Cost)
	Input      float64 `toml:"input"`       // per 1M uncached prompt tokens
	Output     float64 `toml:"output"`      // per 1M completion tokens
	Currency   string  `toml:"currency"`
}

// Cost estimates the spend for a usage record.
func (p *Pricing) Cost(u *Usage) float64 {
	if p == nil || u == nil {
		return 0
	}
	// Cache writes are billed above the input rate (Anthropic 5m write is 1.25×
	// input). When the user hasn't set cache_write, default to 1.25× Input so
	// Anthropic cache-creation isn't silently under-billed as plain input.
	cacheWriteRate := p.CacheWrite
	if cacheWriteRate == 0 && p.Input > 0 {
		cacheWriteRate = 1.25 * p.Input
	}
	promptCost := float64(u.CacheHitTokens)*p.CacheHit +
		float64(u.CacheWriteTokens)*cacheWriteRate +
		float64(u.CacheMissTokens)*p.Input
	// When the provider reports no cache split (all zero), treat all prompt
	// tokens as full-price input — the cost is non-zero even when caching is
	// unavailable or unreported (e.g. some providers omit cache fields).
	if promptCost == 0 && u.PromptTokens > 0 {
		promptCost = float64(u.PromptTokens) * p.Input
	}
	return (promptCost + float64(u.CompletionTokens)*p.Output) / 1e6
}

// Symbol returns the currency display symbol, defaulting to "¥".
// Normalizes common ISO codes to their symbol equivalents.
func (p *Pricing) Symbol() string {
	if p == nil || p.Currency == "" {
		return "¥"
	}
	return currencySymbol(p.Currency)
}

// currencySymbol normalizes common ISO 4217 currency codes to display symbols.
func currencySymbol(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "USD", "US":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "CNY", "RMB":
		return "¥"
	case "JPY":
		return "¥"
	case "KRW":
		return "₩"
	case "INR":
		return "₹"
	default:
		// If the code is already a single Unicode rune (e.g. "€"), pass through.
		if len([]rune(code)) == 1 {
			return code
		}
		return strings.ToUpper(code)
	}
}

// Chunk is a single streamed event. Read the field matching Type.
type Chunk struct {
	Type      ChunkType
	Text      string    // ChunkText, ChunkReasoning
	Signature string    // ChunkReasoning: opaque proof for the reasoning (Anthropic thinking signature), when issued
	ToolCall  *ToolCall // ChunkToolCallStart (ID+Name only), ChunkToolCall (complete)
	Usage     *Usage    // ChunkUsage
	Err       error     // ChunkError
}

// StreamInterruptedError marks a recoverable transport cut that happened after
// the caller had already received model output. Providers must not replay these
// requests themselves because doing so could duplicate visible text or tool
// calls; the agent can append a tail recovery prompt instead.
type StreamInterruptedError struct {
	Err error
}

func (e *StreamInterruptedError) Error() string {
	if e == nil || e.Err == nil {
		return "stream interrupted"
	}
	return e.Err.Error()
}

func (e *StreamInterruptedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsStreamInterrupted(err error) bool {
	var interrupted *StreamInterruptedError
	return errors.As(err, &interrupted)
}

// Provider is a chat-capable model backend.
type Provider interface {
	// Name returns the provider instance name, e.g. "openai" / "anthropic".
	Name() string
	// Stream starts a streaming completion, pushing increments on the channel.
	// Cancelling ctx must abort the underlying request; a closed channel marks
	// the end of the completion.
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

// Config is a resolved provider instance configuration.
type Config struct {
	Name    string         // instance name, e.g. "openai"
	BaseURL string         // OpenAI-compatible endpoint
	Model   string         // model id
	APIKey  string         // resolved from api_key_env
	Extra   map[string]any // kind-specific options
}

// AuthError reports that a provider rejected the API key (HTTP 401/403). Its
// message is already user-facing and actionable — it names the provider and,
// when known, the environment variable the key comes from — so the CLI can
// surface it verbatim instead of dumping a raw status body. Providers should
// return this (rather than a generic status error) for auth failures.
type AuthError struct {
	Provider string // the provider instance name, e.g. "openai"
	KeyEnv   string // the api_key_env the key is read from, when known
	Status   int    // the HTTP status (401 or 403)
	HasKey   bool   // a non-empty key was sent vs. no key configured
}

func (e *AuthError) Error() string {
	key := "the API key"
	if e.KeyEnv != "" {
		key = e.KeyEnv
	}
	return fmt.Sprintf("authentication failed for provider %q (HTTP %d): %s is invalid or expired — update it (in .env or your environment) and retry, or run `fairpeer setup`",
		e.Provider, e.Status, key)
}

// Factory builds a Provider from a resolved Config.
type Factory func(cfg Config) (Provider, error)

var registry = map[string]Factory{}

// Register adds a factory under a kind (e.g. "openai"). Intended for init().
// It panics on a duplicate kind, since that is a compile-time wiring mistake.
func Register(kind string, f Factory) {
	if _, dup := registry[kind]; dup {
		panic("provider: duplicate kind " + kind)
	}
	registry[kind] = f
}

// New instantiates the provider of the given kind.
func New(kind string, cfg Config) (Provider, error) {
	f, ok := registry[kind]
	if !ok {
		return nil, fmt.Errorf("provider: unknown kind %q (registered: %v)", kind, Kinds())
	}
	p, err := f(cfg)
	if err != nil {
		return nil, err
	}
	if nilutil.IsNil(p) {
		return nil, fmt.Errorf("provider: factory %q returned nil provider", kind)
	}
	return p, nil
}

// Kinds returns the registered kinds, sorted.
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
