package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zzycxz/hiq/internal/nilutil"
	"github.com/zzycxz/hiq/internal/provider"
)

const autoPlanClassifierPrompt = `You classify whether a coding-agent user request should first enter read-only planning mode.
Return ONLY JSON: {"needs_plan":true|false,"reason":"short reason"}.
Use true for multi-step implementation, refactors, migrations, unclear cross-file work, PRD/spec/issue work, or tasks needing investigation before edits.
Use false for explanations, simple questions, single obvious edits, direct commands, or requests that should be answered without changing files.`

type ProviderAutoPlanClassifier struct {
	prov provider.Provider
}

func NewProviderAutoPlanClassifier(prov provider.Provider) *ProviderAutoPlanClassifier {
	if nilutil.IsNil(prov) {
		return nil
	}
	return &ProviderAutoPlanClassifier{prov: prov}
}

func (c *ProviderAutoPlanClassifier) NeedsPlan(ctx context.Context, input string, score int) (bool, string, error) {
	if c == nil || nilutil.IsNil(c.prov) {
		return false, "", fmt.Errorf("auto plan classifier is not initialized")
	}
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: autoPlanClassifierPrompt},
		{Role: provider.RoleUser, Content: fmt.Sprintf("heuristic_score=%d\n\nUSER_REQUEST:\n%s", score, input)},
	}
	// Structured output first (upgrade spec 4-7): constrain the reply to the
	// exact schema so no brace-scraping is needed. On a provider that rejects
	// (or ignores) response_format, fall back to the plain request — the
	// JSON-prompt + extractJSONObject path below still handles free text.
	ch, err := c.prov.Stream(ctx, provider.Request{
		Messages:       messages,
		Temperature:    0,
		MaxTokens:      80,
		ResponseSchema: autoPlanSchema,
		SchemaName:     "plan_decision",
	})
	if err == nil {
		var text strings.Builder
		var streamErr error
		for chunk := range ch {
			switch chunk.Type {
			case provider.ChunkText:
				text.WriteString(chunk.Text)
			case provider.ChunkError:
				streamErr = chunk.Err
			}
		}
		if streamErr == nil {
			return decodePlanDecision(text.String())
		}
		if !schemaRejected(streamErr) {
			return false, "", streamErr
		}
	}
	ch, err = c.prov.Stream(ctx, provider.Request{
		Messages:    messages,
		Temperature: 0,
		MaxTokens:   80,
	})
	if err != nil {
		return false, "", err
	}

	var text strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkError:
			return false, "", chunk.Err
		}
	}

	return decodePlanDecision(text.String())
}

// autoPlanSchema pins the classifier's reply shape (upgrade spec 4-7).
var autoPlanSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"needs_plan": {"type": "boolean"},
		"reason": {"type": "string"}
	},
	"required": ["needs_plan"],
	"additionalProperties": false
}`)

// decodePlanDecision parses the (schema-constrained or scraped) reply.
func decodePlanDecision(text string) (bool, string, error) {
	var out struct {
		NeedsPlan *bool  `json:"needs_plan"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(text)), &out); err != nil {
		return false, "", fmt.Errorf("decode classifier response: %w", err)
	}
	if out.NeedsPlan == nil {
		return false, "", fmt.Errorf("decode classifier response: missing needs_plan")
	}
	return *out.NeedsPlan, strings.TrimSpace(out.Reason), nil
}

// schemaRejected reports whether an error looks like the provider refusing
// the response_format parameter (unsupported vendors 400 on it) — the signal
// to retry once without structured output.
func schemaRejected(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "response_format") ||
		strings.Contains(msg, "json_schema") ||
		(strings.Contains(msg, "400") && strings.Contains(msg, "unsupported"))
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end >= start {
		return s[start : end+1]
	}
	return s
}
