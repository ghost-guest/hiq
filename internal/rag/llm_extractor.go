package rag

// llm_extractor.go is the default rag.Extractor implementation: it calls
// the configured OpenAI-compatible /chat/completions endpoint with a JSON-output
// instruction and parses the structured entities+relations out of the response.
//
// This lives in the rag package (not internal/tool/builtin) so it's reusable by
// the Pipeline without a circular dep on the tool layer. It depends only on
// the shared HTTP helper.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zzycxz/hiq/internal/apihelper"
)

// LLMExtractorConfig configures the LLM-backed extractor.
type LLMExtractorConfig struct {
	BaseURL  string // e.g. "https://api.example.com/v1" ("" = uses apihelper.BaseURL)
	APIKey   string // env var name to read the key from (e.g. "HIQ_API_KEY"); if empty, reads HIQ_API_KEY
	Model    string // chat model to use (e.g. the cowork main model)
	TwoStage bool   // extract entities then relations in two LLM calls (higher quality, 2× tokens); false = single combined call
}

// llmExtractor implements rag.Extractor via /chat/completions + JSON mode.
type llmExtractor struct {
	cfg     LLMExtractorConfig
	baseURL string
	client  *http.Client
	// budget gates LLM calls through the global RPM limiter so extraction
	// shares the same per-minute quota as the main agent, subagents, and
	// dream/distill. nil = limiting disabled (no blocking). Set by boot.go
	// via SetBudget; extraction runs at background priority (false) so it
	// doesn't starve the interactive conversation.
	budget    BudgetAcquirer
	budgetKey string
}

// SetBudget installs the global RPM limiter. Implements rag.BudgetSetter.
func (e *llmExtractor) SetBudget(b BudgetAcquirer, key string) {
	e.budget = b
	e.budgetKey = key
}

// NewLLMExtractor builds the default extractor. The cfg passed from boot.go
// carries the model from [cowork] extract_model (or the main model when empty).
func NewLLMExtractor(cfg LLMExtractorConfig) Extractor {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = apihelper.BaseURL
	}
	return &llmExtractor{
		cfg:     cfg,
		baseURL: base,
		client:  &http.Client{Timeout: 180 * time.Second},
	}
}

// Extract calls /chat/completions and parses entities + relations out of the
// assistant's JSON response. In two-stage mode (cfg.TwoStage) it runs two calls
// — entities first, then relations seeded with the extracted entities as
// {known_nodes} — which forces relation endpoints to be real entities and cuts
// hallucinated edges (mirrors HE graph.py:510). Two-stage costs ~2× tokens but
// markedly improves graph quality; single-stage is the cheaper fallback.
func (e *llmExtractor) Extract(ctx context.Context, chunk string, nodePrompt, edgePrompt string) (ExtractResult, error) {
	if strings.TrimSpace(chunk) == "" {
		return ExtractResult{}, nil
	}
	if e.cfg.Model == "" {
		return ExtractResult{}, fmt.Errorf("extract model not configured (set [cowork] extract_model)")
	}
	apiKey := resolveKey(e.cfg.APIKey)
	if apiKey == "" {
		return ExtractResult{}, fmt.Errorf("LLM api key not set (HIQ_API_KEY)")
	}

	text := truncateChunk(chunk, 6000)

	// Resolve prompts: use custom if provided, otherwise built-in defaults.
	np := nodePrompt
	if np == "" {
		np = NodeExtractionPrompt
	}
	ep := edgePrompt
	if ep == "" {
		ep = EdgeExtractionPrompt
	}

	if !e.cfg.TwoStage {
		// Single combined call — use nodePrompt for the combined extraction
		// when a custom prompt is provided, otherwise use the default.
		singlePrompt := ExtractionPrompt
		if nodePrompt != "" {
			singlePrompt = nodePrompt
		}
		content, err := e.chatJSON(ctx, apiKey, fmt.Sprintf(singlePrompt, text))
		if err != nil {
			return ExtractResult{}, err
		}
		res, err := ParseExtractJSON([]byte(content))
		if err != nil {
			return ExtractResult{}, fmt.Errorf("parse extract output: %w (content: %s)", err, truncateStr(content, 200))
		}
		return res, nil
	}

	// Two-stage: entities → relations seeded with the entity list.
	nodeContent, err := e.chatJSON(ctx, apiKey, fmt.Sprintf(np, text))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("stage1 (nodes): %w", err)
	}
	res, err := parseNodesJSON([]byte(nodeContent))
	if err != nil {
		// Fall back to a single-stage parse so a stage-1 format glitch doesn't
		// waste the call entirely.
		res, err = ParseExtractJSON([]byte(nodeContent))
		if err != nil {
			return ExtractResult{}, fmt.Errorf("parse stage1 nodes: %w (content: %s)", err, truncateStr(nodeContent, 200))
		}
	}
	if len(res.Entities) == 0 {
		return res, nil // no entities → no relations to extract
	}
	knownNodes := formatKnownNodes(res.Entities)
	edgeContent, err := e.chatJSON(ctx, apiKey, fmt.Sprintf(ep, knownNodes, text))
	if err != nil {
		// Stage 2 failed: keep the entities we got, skip relations.
		return res, fmt.Errorf("stage2 (edges): %w", err)
	}
	rels, err := parseRelationsJSON([]byte(edgeContent))
	if err != nil {
		// Keep entities; relations just empty.
		return res, nil
	}
	res.Relations = rels
	return res, nil
}

// chatJSON sends one user message and returns the assistant's content string
// (JSON fences stripped). Shared by both single- and two-stage paths.
func (e *llmExtractor) chatJSON(ctx context.Context, apiKey, userMsg string) (string, error) {
	// Gate through the global RPM limiter so extraction shares the per-minute
	// quota with all other LLM calls. Background priority (false) so extraction
	// doesn't starve the interactive conversation under tight RPM limits.
	if e.budget != nil {
		if err := e.budget.Acquire(ctx, e.budgetKey, false); err != nil {
			return "", fmt.Errorf("extract rate-limited: %w", err)
		}
	}
	reqBody := chatCompletionsRequest{
		Model: e.cfg.Model,
		Messages: []chatMessage{
			{Role: "user", Content: userMsg},
		},
		Temperature:    0,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("extract http: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("extract HTTP %d: %s", resp.StatusCode, truncateStr(string(respBytes), 300))
	}
	var cr chatCompletionsResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", fmt.Errorf("parse chat response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return stripCodeFence(cr.Choices[0].Message.Content), nil
}

// formatKnownNodes renders the stage-1 entity list as the bullet list injected
// into the stage-2 prompt as {known_nodes} (HE graph.py:611 uses the same
// "- name\n- name" shape). Uses the raw display name so the model can match it
// verbatim against the text.
func formatKnownNodes(entities []Entity) string {
	if len(entities) == 0 {
		return "（本段未识别到具体实体）"
	}
	var b strings.Builder
	for _, e := range entities {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(e.NameRaw))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// parseNodesJSON parses a stage-1 (entities-only) LLM response into an
// ExtractResult carrying just entities. Tolerates the canonical {entities:[...]}
// shape even if a relations field is also present (we ignore it).
func parseNodesJSON(b []byte) (ExtractResult, error) {
	var raw struct {
		Entities []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return ExtractResult{}, fmt.Errorf("parse nodes json: %w", err)
	}
	res := ExtractResult{}
	for _, e := range raw.Entities {
		res.Entities = append(res.Entities, Entity{
			NameRaw:     e.Name,
			Type:        e.Type,
			Description: e.Description,
		})
	}
	return res, nil
}

// parseRelationsJSON parses a stage-2 (relations-only) LLM response.
func parseRelationsJSON(b []byte) ([]Relation, error) {
	var raw struct {
		Relations []struct {
			Source      string  `json:"source"`
			Target      string  `json:"target"`
			Type        string  `json:"type"`
			Description string  `json:"description"`
			Strength    float64 `json:"strength"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse relations json: %w", err)
	}
	var out []Relation
	for _, r := range raw.Relations {
		out = append(out, Relation{
			Source:      r.Source,
			Target:      r.Target,
			Type:        r.Type,
			Description: r.Description,
			Strength:    r.Strength,
		})
	}
	return out, nil
}

// resolveKey reads the API key from the named env var (default HIQ_API_KEY).
func resolveKey(envName string) string {
	if envName == "" {
		envName = "HIQ_API_KEY"
	}
	return strings.TrimSpace(os.Getenv(envName))
}

// truncateAtByte returns s truncated to at most max bytes, then appends "…".
// It backs up to a UTF-8 rune boundary so it never cuts a multibyte CJK char in
// half (which would emit invalid UTF-8 / mojibake to the model).
func truncateAtByte(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func truncateChunk(s string, max int) string {
	return truncateAtByte(s, max)
}

func truncateStr(s string, n int) string {
	return truncateAtByte(s, n)
}

// stripCodeFence removes ```json ... ``` wrappers that some models add despite
// the json_object instruction.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	for _, fence := range []string{"```json", "```JSON", "```"} {
		if strings.HasPrefix(s, fence) {
			s = strings.TrimPrefix(s, fence)
			break
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// --- request/response shapes (OpenAI-compatible /chat/completions) ----------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"` // "json_object" | "text"
}

type chatCompletionsRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatCompletionsResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}
