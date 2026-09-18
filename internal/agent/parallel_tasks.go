package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

// ParallelTasksTool dispatches multiple sub-agent tasks concurrently and
// collects all results. Each sub-task runs as a foreground sub-agent in its own
// goroutine, emitting nested events so the frontend renders independent cards
// per sub-task. It reuses TaskTool's sub-agent infrastructure (provider
// resolution, tool filtering, transcript runs) so every sub-task inherits the
// same sandbox, gate, and hooks — only the dispatch is parallel.
//
// Multi-model fit: each sub-task accepts an optional model/effort override, so a caller
// can route independent pieces of work to different models on the same platform
// — e.g. planning to a large reasoning model, code generation to a code-tuned
// model, search/routing to a small fast model. The TaskTool.resolveProvider
// callback resolves each per-sub-task model just like a single task call would.
//
// Ported from DeepSeek-Reasonix (parallel_tasks tool), adapted to hiq's
// TaskTool shape. Read-only classification matches the upstream tool so the
// agent's parallel-batch optimizer runs these concurrently without write races;
// a sub-agent that needs to write should use the sequential `task` tool.
type ParallelTasksTool struct {
	taskTool *TaskTool
	reg      *tool.Registry
}

// NewParallelTasksTool creates a parallel dispatch tool that reuses the given
// TaskTool's sub-agent infrastructure. reg is the parent registry the per-task
// tool whitelists are filtered from.
func NewParallelTasksTool(taskTool *TaskTool, reg *tool.Registry) *ParallelTasksTool {
	return &ParallelTasksTool{taskTool: taskTool, reg: reg}
}

func (p *ParallelTasksTool) Name() string { return "parallel_tasks" }

func (p *ParallelTasksTool) Description() string {
	return "Dispatch multiple sub-agent tasks concurrently and collect their results. Each task runs in its own sub-agent in parallel, blocks until all complete. Each task may override the model/effort, so independent pieces of a job can be routed to different models (e.g. a reasoning model for planning, a code model for implementation). Prefer this for independent research/exploration across separate areas; use the sequential `task` tool when sub-tasks have ordering or write dependencies."
}

func (p *ParallelTasksTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "tasks":{
	"type":"array",
	"description":"Array of sub-task descriptions to run in parallel.",
	"items":{
	  "type":"object",
	  "properties":{
		"prompt":{"type":"string","description":"The task prompt for the sub-agent."},
		"description":{"type":"string","description":"Optional short label shown in the job list."},
		"tools":{"type":"array","items":{"type":"string"},"description":"Optional tool whitelist for the sub-agent."},
		"max_steps":{"type":"integer","description":"Optional max tool-call rounds.","minimum":1},
		"model":{"type":"string","description":"Optional model override — route this sub-task to a different model on the platform."},
		"effort":{"type":"string","description":"Optional reasoning effort override."}
	  },
	  "required":["prompt"]
	}
  }
},
"required":["tasks"]
}`)
}

// ReadOnly is true: the parallel-dispatch path only collects results and must
// not let concurrent writes race. Sub-agents that need to mutate state should go
// through the sequential `task` tool, where ordering is preserved. This also
// lets the agent's parallel-batch optimizer run parallel_tasks alongside other
// read-only calls.
func (p *ParallelTasksTool) ReadOnly() bool { return true }

type parallelTaskArg struct {
	Prompt      string   `json:"prompt"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
	MaxSteps    int      `json:"max_steps"`
	Model       string   `json:"model"`
	Effort      string   `json:"effort"`
}

func (p *ParallelTasksTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var pArgs struct {
		Tasks []parallelTaskArg `json:"tasks"`
	}
	if err := json.Unmarshal(args, &pArgs); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if len(pArgs.Tasks) == 0 {
		return "", fmt.Errorf("tasks must be a non-empty array")
	}

	parentID, parent, _, _ := CallContext(ctx)
	parentSession := ParentSession(ctx)

	type result struct {
		desc   string
		answer string
		err    error
	}
	results := make([]result, len(pArgs.Tasks))
	var wg sync.WaitGroup
	for i, t := range pArgs.Tasks {
		if strings.TrimSpace(t.Prompt) == "" {
			results[i] = result{desc: t.Description, err: fmt.Errorf("task %d: prompt is required", i)}
			continue
		}
		// Each sub-task gets its own nested sink so the frontend renders an
		// independent card per sub-task (the same nesting `task` uses, but the
		// parent ID is namespaced per sub-task index so concurrent sub-tasks
		// don't collide in dispatch→result matching).
		nestedParentID := fmt.Sprintf("%s/p%d", parentID, i)
		sink := subSinkFor(nestedParentID, parent)

		maxSteps := t.MaxSteps
		if maxSteps <= 0 {
			if p.taskTool.maxSteps > 0 {
				maxSteps = p.taskTool.maxSteps / 2
				if maxSteps < 5 {
					maxSteps = 5
				}
			}
		}
		subReg := FilterRegistry(p.reg, t.Tools, SubagentMetaTools()...)
		modelRef, effortRef := p.taskTool.effectiveProfile(t.Model, t.Effort)
		prov, pricing, ctxWin, err := p.taskTool.resolveSubSessionRuntime(modelRef, effortRef)
		if err != nil {
			results[i] = result{desc: labelFor(t.Description, i), err: fmt.Errorf("sub-agent profile: %w", err)}
			continue
		}
		// Fresh ephemeral transcript per sub-task — parallel sub-tasks must not
		// share a transcript reference (continue/fork semantics don't apply to a
		// one-shot parallel dispatch).
		run := EphemeralSubagentRun(p.taskTool.sysPrompt)
		_ = parentSession // EphemeralSubagentRun needs no parent session

		wg.Add(1)
		go func(idx int, task parallelTaskArg, sess *Session, subProv provider.Provider, subPricing *provider.Pricing, subCtxWin int, ms int, taskSink event.Sink) {
			defer wg.Done()
			answer, err := p.taskTool.runSubSession(ctx, task.Prompt, subReg, taskSink, ms, subProv, subPricing, subCtxWin, sess)
			results[idx] = result{desc: labelFor(task.Description, idx), answer: answer, err: err}
		}(i, t, run.Session, prov, pricing, ctxWin, maxSteps, sink)
	}
	wg.Wait()

	// Aggregate. A per-task error is reported inline rather than failing the whole
	// call — partial results are useful and the model can decide whether to retry.
	var b strings.Builder
	allFailed := true
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		header := r.desc
		if header == "" {
			header = fmt.Sprintf("Task %d", i+1)
		}
		if r.err != nil {
			fmt.Fprintf(&b, "### %s\n\n[failed: %s]", header, r.err.Error())
			continue
		}
		allFailed = false
		fmt.Fprintf(&b, "### %s\n\n%s", header, r.answer)
	}
	if allFailed && len(results) > 0 {
		// Every sub-task failed: surface as an error so the caller knows nothing
		// was produced, but keep the per-task breakdown in the message.
		return b.String(), errors.New("all parallel sub-tasks failed")
	}
	return b.String(), nil
}

func labelFor(desc string, idx int) string {
	if strings.TrimSpace(desc) != "" {
		return desc
	}
	return fmt.Sprintf("Task %d", idx+1)
}
