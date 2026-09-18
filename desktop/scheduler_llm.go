package main

// scheduler_llm.go adds an LLM-backed natural-language time parser that resolves
// phrases the regex parser can't ("下下周五下午3点", "两个星期后", "下个月初").
// It is invoked ONLY by App.SmartParseSchedule, which the UI calls from an
// explicit "🔍 智能解析" button click — never during typing. The cheap regex path
// (internal/scheduler/reltime.go, today/明天/8点50/15:00…) still handles every
// keystroke; this LLM parse is the on-demand upgrade for complex phrases.
//
// Architecture note: this mirrors App.RagAsk (desktop/rag_app.go) — a direct
// /chat/completions POST on the fast_task_model (迅捷任务模型)
// with temperature:0, gated through the global RPM budget, NOT through
// provider.SendWithRetry (whose 10x backoff would blow the UI's responsiveness
// budget). The call is synchronous because SmartParseSchedule is a sync Wails
// binding, but it carries a short timeout so a slow model never hangs the button.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/apihelper"
	"github.com/zzycxz/hiq/internal/boot"
	"github.com/zzycxz/hiq/internal/config"
)

// llmResolveTimeTimeout bounds a single LLM time-parse call. PreviewSchedule is a
// live UI call (debounced 200ms in TaskForm), so we stay tight; if the model is
// slow we'd rather show the raw phrase than freeze the input.
const llmResolveTimeTimeout = 8 * time.Second

// llmParseTime asks the fast_task_model to resolve a natural-language time phrase
// into an absolute instant. Returns a zero time + nil error when the model says
// the text isn't a time phrase (so the caller can keep showing "unknown" rather
// than a bogus date).
//
// now is the reference instant the phrase is resolved against (today/now); the
// caller passes time.Now(). Provider/key resolution follows the RagAsk path:
// fast_task_model → default_model → error if not configured.
func llmParseTime(ctx context.Context, text string, now time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, nil
	}

	// Resolve the fast model + credentials (same chain as RagAsk / extraction).
	cfg, _ := config.Load()
	var baseURL, apiKeyEnv, modelName string
	if cfg != nil {
		modelRef := strings.TrimSpace(cfg.Agent.FastTaskModel)
		if modelRef == "" {
			modelRef = strings.TrimSpace(cfg.DefaultModel)
		}
		if modelRef != "" {
			if e, ok := cfg.ResolveModel(modelRef); ok {
				baseURL, apiKeyEnv, modelName = e.BaseURL, e.APIKeyEnv, e.Model
			}
		}
	}
	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		apiKey = os.Getenv("HIQ_API_KEY")
	}
	if apiKey == "" {
		return time.Time{}, fmt.Errorf("LLM api key not configured")
	}
	if baseURL == "" {
		baseURL = apihelper.BaseURL
	}
	if modelName == "" {
		return time.Time{}, fmt.Errorf("no LLM model configured: set fast_task_model or default_model in hiq.toml")
	}

	// Strict output contract: one line "YYYY-MM-DD HH:MM", or "N/A". We give the
	// model the wall-clock now (with weekday) so relative words ("后天", "下下周
	// 三") anchor correctly, and forbid it from inventing past dates.
	nowStr := now.Format("2006-01-02 15:04 (Mon)")
	systemPrompt := fmt.Sprintf(`你是一个时间解析器。用户会给你一句中文/英文里包含的时间表达，你的任务是把它换算成一个绝对时间。

当前时间：%s

规则：
1. 只输出一个绝对时间，格式严格为 "YYYY-MM-DD HH:MM"（24小时制），不要任何其他文字、标点或解释
2. 解析的相对词（今天/明天/后天/下周/下下周/两个星期后/月底/下个月等）都以"当前时间"为基准计算
3. 如果句子里的时间已经过去，按"最近的未来同一时刻"理解（例如现在是上午10点，"8点"→明天8点）
4. 如果输入根本不是时间表达（比如"你好"、"红色"），只输出 "N/A"
5. 如果时间是模糊的无法确定，只输出 "N/A"`, nowStr)

	chatReq := map[string]any{
		"model":       modelName,
		"temperature": 0,
		"max_tokens":  40,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": text},
		},
	}
	body, _ := json.Marshal(chatReq)

	// Bound the call so a slow model never blocks the UI handler long.
	apiCtx, cancel := context.WithTimeout(ctx, llmResolveTimeTimeout)
	defer cancel()

	// Share the fast-model RPM quota with the rest of the app (background
	// priority so a preview can't starve the main conversation).
	if b := boot.GlobalBudget(); b != nil {
		if err := b.Acquire(apiCtx, boot.RagAskBudgetKey(cfg), false); err != nil {
			return time.Time{}, fmt.Errorf("time-parse rate-limited: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(apiCtx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := apihelper.Client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return time.Time{}, fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, apihelper.Truncate(string(respBody), 300))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return time.Time{}, fmt.Errorf("parse LLM response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return time.Time{}, fmt.Errorf("LLM returned no choices")
	}

	// Pull the YYYY-MM-DD HH:MM out of whatever the model emitted. Delegated to
	// extractLLMTime so the parsing logic is unit-testable without a network call.
	t, ok := extractLLMTime(chatResp.Choices[0].Message.Content, now)
	if !ok {
		return time.Time{}, nil
	}
	return t, nil
}

// extractLLMTime pulls the first parseable absolute time out of the model's
// free-form response. ok=false means the response was "N/A" / not a time phrase
// (the caller treats this as "unrecognized", distinct from a transport error).
// Defensive against the common ways models deviate from the bare-format
// instruction: wrapping in quotes/markdown, prefixing prose, emitting seconds,
// or using slashes. now supplies the timezone for the parsed instant.
func extractLLMTime(content string, now time.Time) (time.Time, bool) {
	raw := strings.TrimSpace(content)
	raw = strings.Trim(raw, "`\"' \n\r\t")
	raw = strings.Trim(raw, "*") // strip stray markdown ("**2026-…**")
	if raw == "" || strings.Contains(strings.ToUpper(raw), "N/A") {
		return time.Time{}, false
	}
	for _, m := range reDateTime.FindAllString(raw, -1) {
		// Normalize an ISO-style "T" date/time separator to a space so Go's
		// space-based layouts accept it.
		cand := strings.Replace(m, "T", " ", 1)
		for _, layout := range []string{
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006/01/02 15:04:05",
			"2006/01/02 15:04",
		} {
			if t, err := time.ParseInLocation(layout, cand, now.Location()); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// reDateTime matches a "YYYY-MM-DD HH:MM" or "YYYY-MM-DD HH:MM:SS" token (and the
// slash variant), tolerating an optional T separator. Used to robustly pull the
// absolute time out of the model's free-form response.
var reDateTime = regexp.MustCompile(`\d{4}[-/]\d{1,2}[-/]\d{1,2}[ T]\d{1,2}:\d{2}(?::\d{2})?`)
