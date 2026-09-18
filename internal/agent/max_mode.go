package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/provider"
)

const (
	// DefaultMaxCandidates is the number of parallel propose-only candidates.
	DefaultMaxCandidates = 5
	// maxModeJudgeTimeout bounds the judge call.
	maxModeJudgeTimeout = 60 * time.Second
)

// MaxCandidate holds one candidate's streamed response.
type MaxCandidate struct {
	Index     int
	Text      string
	Reasoning string
	ToolCalls []provider.ToolCall
	Usage     provider.Usage
	Err       error
}

// MaxJudgeResult is the judge's selection.
type MaxJudgeResult struct {
	BestIndex int    `json:"best"`
	Reason    string `json:"reason"`
}

const maxModeJudgeSystemPrompt = `You are a judge evaluating parallel reasoning candidates. You will receive N candidate responses to the same user prompt. Each candidate proposed tool calls (but did not execute them).

Select the SINGLE BEST candidate based on:
1. Quality of reasoning — is the approach sound, complete, and efficient?
2. Correctness of proposed tool calls — are the right tools chosen with correct arguments?
3. Completeness — does the candidate address all aspects of the user's request?

Respond with JSON: {"best": <0-indexed integer>, "reason": "<brief explanation>"}
Pick exactly one candidate by its index number.`

// RunMaxStep runs N parallel propose-only candidates, then a judge selects the
// best one. The winner's tool calls are returned for actual execution. If all
// candidates fail, returns nil (caller should fall back to normal single-step).
func RunMaxStep(ctx context.Context, prov provider.Provider, sysPrompt string, messages []provider.Message, tools []provider.ToolSchema, n int, temperature float64) *MaxJudgeResult {
	if n <= 1 {
		n = DefaultMaxCandidates
	}

	// Build propose-only tool descriptions (schema only, no execution).
	toolDesc := describeToolsForJudge(tools)

	// Run N candidates in parallel.
	candidates := make([]*MaxCandidate, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			candidates[idx] = runCandidate(ctx, prov, sysPrompt, messages, toolDesc, temperature, idx)
		}(i)
	}
	wg.Wait()

	// Filter survivors.
	var survivors []*MaxCandidate
	for _, c := range candidates {
		if c != nil && c.Err == nil && (c.Text != "" || len(c.ToolCalls) > 0) {
			survivors = append(survivors, c)
		}
	}
	if len(survivors) == 0 {
		return nil // All failed; caller falls back.
	}
	if len(survivors) == 1 {
		return &MaxJudgeResult{BestIndex: survivors[0].Index, Reason: "only survivor"}
	}

	// Judge picks the best.
	return runMaxJudge(ctx, prov, survivors, temperature)
}

func runCandidate(ctx context.Context, prov provider.Provider, sysPrompt string, messages []provider.Message, toolDesc string, temperature float64, idx int) *MaxCandidate {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Append tool description to system prompt for propose-only behavior.
	fullSys := sysPrompt + "\n\n## Available Tools (propose only)\n\n" + toolDesc

	ch, err := prov.Stream(ctx, provider.Request{
		Messages:    append([]provider.Message{{Role: provider.RoleSystem, Content: fullSys}}, messages...),
		Temperature: temperature,
	})
	if err != nil {
		return &MaxCandidate{Index: idx, Err: err}
	}

	c := &MaxCandidate{Index: idx}
	for {
		select {
		case <-ctx.Done():
			c.Err = ctx.Err()
			return c
		case chunk, ok := <-ch:
			if !ok {
				return c
			}
			switch chunk.Type {
			case provider.ChunkText:
				c.Text += chunk.Text
			case provider.ChunkReasoning:
				c.Reasoning += chunk.Text
			case provider.ChunkToolCall:
				if chunk.ToolCall != nil {
					c.ToolCalls = append(c.ToolCalls, *chunk.ToolCall)
				}
			case provider.ChunkError:
				c.Err = chunk.Err
				return c
			case provider.ChunkUsage:
				if chunk.Usage != nil {
					c.Usage = *chunk.Usage
				}
			}
		}
	}
}

func runMaxJudge(ctx context.Context, prov provider.Provider, candidates []*MaxCandidate, temperature float64) *MaxJudgeResult {
	ctx, cancel := context.WithTimeout(ctx, maxModeJudgeTimeout)
	defer cancel()

	var userMsg strings.Builder
	userMsg.WriteString("## Candidates\n\n")
	for _, c := range candidates {
		fmt.Fprintf(&userMsg, "### Candidate %d\n\n", c.Index)
		if c.Reasoning != "" {
			userMsg.WriteString("**Reasoning:** " + c.Reasoning[:min(len(c.Reasoning), 500)] + "\n\n")
		}
		if c.Text != "" {
			userMsg.WriteString("**Response:** " + c.Text[:min(len(c.Text), 1000)] + "\n\n")
		}
		if len(c.ToolCalls) > 0 {
			userMsg.WriteString("**Proposed tool calls:**\n")
			for _, tc := range c.ToolCalls {
				fmt.Fprintf(&userMsg, "- %s(%s)\n", tc.Name, tc.Arguments[:min(len(tc.Arguments), 200)])
			}
			userMsg.WriteString("\n")
		}
	}

	ch, err := prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: maxModeJudgeSystemPrompt},
			{Role: provider.RoleUser, Content: userMsg.String()},
		},
		Temperature: 0, // Deterministic judge.
	})
	if err != nil {
		return &MaxJudgeResult{BestIndex: candidates[0].Index, Reason: "judge failed: " + err.Error()}
	}

	var raw strings.Builder
	for {
		select {
		case <-ctx.Done():
			return &MaxJudgeResult{BestIndex: candidates[0].Index, Reason: "judge timed out"}
		case chunk, ok := <-ch:
			if !ok {
				return parseMaxJudgeResult(raw.String(), candidates)
			}
			if chunk.Type == provider.ChunkText {
				raw.WriteString(chunk.Text)
			}
		}
	}
}

func parseMaxJudgeResult(raw string, candidates []*MaxCandidate) *MaxJudgeResult {
	raw = strings.TrimSpace(raw)
	var r MaxJudgeResult
	if err := json.Unmarshal([]byte(extractJSON(raw)), &r); err != nil {
		return &MaxJudgeResult{BestIndex: candidates[0].Index, Reason: "judge returned unparseable: " + raw}
	}
	// Validate index.
	if r.BestIndex < 0 || r.BestIndex >= len(candidates) {
		return &MaxJudgeResult{BestIndex: candidates[0].Index, Reason: "judge returned invalid index"}
	}
	return &r
}

func describeToolsForJudge(tools []provider.ToolSchema) string {
	var b strings.Builder
	for _, ts := range tools {
		fmt.Fprintf(&b, "- %s: %s\n  Parameters: %s\n", ts.Name, ts.Description, ts.Parameters)
	}
	return b.String()
}
