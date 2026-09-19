package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/tool"
)

var errFake = errors.New("fake failure")

// TestReadRepeatNudgeFiresOnIdenticalReadOnlyBatches runs the same successful
// read-only batch until the guard nudges: the nudge must append to the result
// (advisory — never block) and the counter must reset when a different call
// appears.
func TestReadRepeatNudgeFiresOnIdenticalReadOnlyBatches(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "a", readOnly: true})
	a := New(nil, reg, NewSession(""), Options{}, event.Discard)

	call := []provider.ToolCall{{Name: "a", Arguments: `{"x":1}`}}
	var last string
	for i := 1; i <= readRepeatNudgeThreshold+1; i++ {
		results := a.executeBatch(context.Background(), call)
		last = results[0]
		if i < readRepeatNudgeThreshold && strings.Contains(last, "[loop guard]") {
			t.Fatalf("batch %d nudged too early: %s", i, last)
		}
		if i == readRepeatNudgeThreshold {
			if !strings.Contains(last, "[loop guard]") || !strings.Contains(last, "returned the same result") {
				t.Fatalf("batch %d (threshold) missing nudge: %s", i, last)
			}
			// Advisory: the tool's own output must still be present.
			if !strings.Contains(last, "a done") {
				t.Fatalf("nudge dropped the tool output: %s", last)
			}
		}
	}

	// A different fingerprint resets the streak.
	a.executeBatch(context.Background(), []provider.ToolCall{{Name: "a", Arguments: `{"x":2}`}})
	results := a.executeBatch(context.Background(), call)
	if strings.Contains(results[0], "[loop guard]") {
		t.Fatalf("nudge fired after a different call reset the streak: %s", results[0])
	}
}

// TestReadRepeatNudgeIgnoresWritersAndFailures ensures the guard only watches
// successful read-only calls: any writer, error, or block resets it.
func TestReadRepeatNudgeIgnoresWritersAndFailures(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "ro", readOnly: true})
	reg.Add(fakeTool{name: "rw", readOnly: false})
	reg.Add(fakeTool{name: "boom", readOnly: true, err: errFake})
	a := New(nil, reg, NewSession(""), Options{}, event.Discard)

	same := []provider.ToolCall{{Name: "ro", Arguments: `{"q":1}`}}
	for i := 0; i < readRepeatNudgeThreshold-1; i++ {
		a.executeBatch(context.Background(), same)
	}
	// Interleave a writer: streak resets, no nudge on the next identical batch.
	a.executeBatch(context.Background(), []provider.ToolCall{{Name: "rw"}})
	if res := a.executeBatch(context.Background(), same); strings.Contains(res[0], "[loop guard]") {
		t.Fatalf("writer interleave should reset the streak: %s", res[0])
	}

	for i := 0; i < readRepeatNudgeThreshold-1; i++ {
		a.executeBatch(context.Background(), same)
	}
	// Interleave a failing read-only call: also resets.
	a.executeBatch(context.Background(), []provider.ToolCall{{Name: "boom"}})
	if res := a.executeBatch(context.Background(), same); strings.Contains(res[0], "[loop guard]") {
		t.Fatalf("failed call should reset the streak: %s", res[0])
	}
}
