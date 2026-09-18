package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/provider"
)

const goalJudgeTimeout = 60 * time.Second

// GoalVerdict is the structured response from the independent goal judge.
type GoalVerdict struct {
	OK         bool   `json:"ok"`
	Impossible bool   `json:"impossible,omitempty"`
	Reason     string `json:"reason"`
}

const goalJudgeSystemPrompt = `You are an independent evaluator. You will receive a conversation transcript and a goal condition. Your job is to determine whether the goal has been achieved based solely on the evidence in the transcript.

Rules:
- Read the full transcript carefully. Look for concrete evidence (test results, file changes, command outputs) that the goal is met.
- The assistant claiming the goal is complete is evidence, not proof. Independently confirm by checking for actual tool outputs that verify completion.
- If the goal is genuinely unachievable (self-contradictory, missing required resource that cannot be created), set impossible=true.
- If there is insufficient evidence to confirm completion, set ok=false (not impossible).
- Quote specific transcript evidence in your reason field.
- Be strict: "mostly done" or "the plan is ready" is not completion.`

// GoalJudge calls an independent model to evaluate whether a goal condition has
// been met based on the conversation transcript. The judge is "cold" — it only
// reads the transcript and never does the work itself, preventing optimism bias.
func GoalJudge(ctx context.Context, prov provider.Provider, transcript []provider.Message, condition string, temperature float64) GoalVerdict {
	ctx, cancel := context.WithTimeout(ctx, goalJudgeTimeout)
	defer cancel()

	// Build the judge prompt.
	var userMsg strings.Builder
	userMsg.WriteString("## Goal Condition\n\n")
	userMsg.WriteString(condition)
	userMsg.WriteString("\n\n## Conversation Transcript\n\n")
	userMsg.WriteString(renderTranscript(transcript))
	userMsg.WriteString("\n\nEvaluate whether the goal condition has been achieved. Respond with JSON: {\"ok\": bool, \"impossible\": bool, \"reason\": string}")

	ch, err := prov.Stream(ctx, provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: goalJudgeSystemPrompt},
			{Role: provider.RoleUser, Content: userMsg.String()},
		},
		Temperature: 0, // Deterministic for consistent judgment.
	})
	if err != nil {
		return GoalVerdict{OK: false, Reason: "judge call failed: " + err.Error()}
	}

	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return GoalVerdict{OK: false, Reason: "judge call timed out"}
		case chunk, ok := <-ch:
			if !ok {
				return parseGoalVerdict(b.String())
			}
			switch chunk.Type {
			case provider.ChunkText:
				b.WriteString(chunk.Text)
			case provider.ChunkError:
				return GoalVerdict{OK: false, Reason: "judge stream error: " + chunk.Err.Error()}
			}
		}
	}
}

func parseGoalVerdict(raw string) GoalVerdict {
	raw = strings.TrimSpace(raw)
	var v GoalVerdict
	if err := json.Unmarshal([]byte(extractJSON(raw)), &v); err != nil {
		return GoalVerdict{OK: false, Reason: "judge returned unparseable response: " + raw}
	}
	if v.Reason == "" {
		v.Reason = "judge provided no reason"
	}
	return v
}

// GoalJudgeWithRetry calls the goal judge, retrying once on transient errors.
func GoalJudgeWithRetry(ctx context.Context, prov provider.Provider, transcript []provider.Message, condition string, temperature float64) GoalVerdict {
	v := GoalJudge(ctx, prov, transcript, condition, temperature)
	// If the judge failed due to a transient error (not timeout or parse failure), retry once.
	if !v.OK && !v.Impossible && (strings.Contains(v.Reason, "stream error") || strings.Contains(v.Reason, "call failed")) {
		v = GoalJudge(ctx, prov, transcript, condition, temperature)
	}
	return v
}
