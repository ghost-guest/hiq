package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/event"
	"github.com/zzycxz/hiq/internal/provider"
	"github.com/zzycxz/hiq/internal/usagecatalog"
)

// Output-ceiling observability (open-vetta 移植 N-1).
//
// The question this feature exists to answer is "was the answer cut off because
// I set max_tokens too low, or because the relay cut it?" — so the tests care
// about the three verdicts being distinguished, and about old records staying
// readable.

func TestOutputCeilingVerdictSplitsTheTwoCauses(t *testing.T) {
	cases := []struct {
		name       string
		usage      *provider.Usage
		truncated  bool
		ceilingHit bool
		gatewayCut bool
	}{
		{
			name:  "finished normally",
			usage: &provider.Usage{CompletionTokens: 120, MaxOutputTokens: 4096, FinishReason: "stop"},
		},
		{
			name: "tool call turn",
			usage: &provider.Usage{CompletionTokens: 80, MaxOutputTokens: 4096,
				FinishReason: "tool_calls"},
		},
		{
			// The output filled our ceiling: raising max_tokens is the fix.
			name:       "our ceiling was too low",
			usage:      &provider.Usage{CompletionTokens: 4096, MaxOutputTokens: 4096, FinishReason: "length"},
			truncated:  true,
			ceilingHit: true,
		},
		{
			// Cut short of our ceiling: something in between did it.
			name:       "a relay cut the answer",
			usage:      &provider.Usage{CompletionTokens: 700, MaxOutputTokens: 8192, FinishReason: "length"},
			truncated:  true,
			gatewayCut: true,
		},
		{
			// Truncated with no ceiling on the wire: honest "unknown", counted
			// as truncated but attributed to neither cause.
			name:      "truncated with an unknown ceiling",
			usage:     &provider.Usage{CompletionTokens: 700, MaxOutputTokens: 0, FinishReason: "length"},
			truncated: true,
		},
		{
			name:  "no usage at all",
			usage: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, truncated, ceilingHit, gatewayCut := outputCeilingVerdict(tc.usage)
			if truncated != tc.truncated || ceilingHit != tc.ceilingHit || gatewayCut != tc.gatewayCut {
				t.Fatalf("got truncated=%v ceilingHit=%v gatewayCut=%v, want %v/%v/%v",
					truncated, ceilingHit, gatewayCut, tc.truncated, tc.ceilingHit, tc.gatewayCut)
			}
			if tc.ceilingHit && tc.gatewayCut {
				t.Fatal("the two causes must be mutually exclusive")
			}
		})
	}
}

func TestOutputCeilingVerdictCarriesTheCeilingAndReason(t *testing.T) {
	maxOutput, reason, _, _, _ := outputCeilingVerdict(&provider.Usage{
		CompletionTokens: 100, MaxOutputTokens: 32768, FinishReason: " length ",
	})
	if maxOutput != 32768 {
		t.Errorf("the ceiling must survive: got %d", maxOutput)
	}
	if reason != FinishReasonLength {
		t.Errorf("the stop reason should be normalized, got %q", reason)
	}
}

func TestRecorderPersistsTruncationVerdict(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(&spySink{}, dir, "desktop")

	emit := func(usage *provider.Usage) {
		r.Emit(event.Event{Kind: event.Usage, ModelRef: "deepseek/deepseek-v4-flash", Usage: usage})
	}
	emit(&provider.Usage{CompletionTokens: 4096, TotalTokens: 4200, MaxOutputTokens: 4096, FinishReason: "length"})
	emit(&provider.Usage{CompletionTokens: 700, TotalTokens: 900, MaxOutputTokens: 8192, FinishReason: "length"})
	emit(&provider.Usage{CompletionTokens: 700, TotalTokens: 900, MaxOutputTokens: 0, FinishReason: "length"})
	emit(&provider.Usage{CompletionTokens: 42, TotalTokens: 100, MaxOutputTokens: 4096, FinishReason: "stop"})
	flushRecorder(t, r)

	from := time.Now().AddDate(0, 0, -1)
	stats, err := NewWriter(dir).Query(SourceFilter{From: from, To: time.Now()})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if stats.Truncated != 3 {
		t.Errorf("Truncated = %d, want 3", stats.Truncated)
	}
	if stats.CeilingHit != 1 {
		t.Errorf("CeilingHit = %d, want 1", stats.CeilingHit)
	}
	if stats.GatewayCut != 1 {
		t.Errorf("GatewayCut = %d, want 1", stats.GatewayCut)
	}
	if stats.Requests != 4 {
		t.Errorf("Requests = %d, want 4", stats.Requests)
	}

	// The raw fields must be on disk too, so the decision can be re-derived
	// later (or by an external tool) rather than only read as a count.
	data, err := os.ReadFile(filepath.Join(dir, time.Now().Format(dayLayout)+".jsonl"))
	if err != nil {
		t.Fatalf("read stats file: %v", err)
	}
	if !strings.Contains(string(data), `"stop_reason":"length"`) {
		t.Errorf("the stop reason must be persisted:\n%s", data)
	}
	if !strings.Contains(string(data), `"max_output":8192`) {
		t.Errorf("the ceiling must be persisted:\n%s", data)
	}
}

func TestQueryToleratesLegacyRecordsWithoutCeilingFields(t *testing.T) {
	dir := t.TempDir()
	// A row written before this feature shipped: token fields only, no
	// truncation keys. Reading it must not error and must not invent a verdict.
	legacy := `{"ts":"2026-08-01T10:00:00Z","model":"deepseek/x","source":"desktop","prompt":10,"completion":20,"total":30}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-08-01.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	day, _ := time.Parse(dayLayout, "2026-08-01")
	stats, err := NewWriter(dir).Query(SourceFilter{From: day, To: day})
	if err != nil {
		t.Fatalf("query legacy file: %v", err)
	}
	if stats.Tokens != 30 || stats.Requests != 1 {
		t.Fatalf("legacy totals should still aggregate: %+v", stats)
	}
	if stats.Truncated != 0 || stats.CeilingHit != 0 || stats.GatewayCut != 0 {
		t.Errorf("a legacy row must not contribute a verdict: %+v", stats)
	}
}

func TestUsageEntryCarriesTruncationCounters(t *testing.T) {
	entry := usageEntry("2026-09-18", record{
		ModelRef:   "deepseek/x",
		Total:      10,
		Truncated:  true,
		CeilingHit: true,
	})
	if entry.Truncated != 1 || entry.CeilingHit != 1 || entry.GatewayCut != 0 {
		t.Fatalf("catalog entry should mirror the verdict: %+v", entry)
	}
	plain := usageEntry("2026-09-18", record{ModelRef: "deepseek/x", Total: 10})
	if plain.Truncated != 0 || plain.CeilingHit != 0 || plain.GatewayCut != 0 {
		t.Fatalf("an untruncated record must not count: %+v", plain)
	}
}

func TestRangeStatsFromRollupsSumsTruncationCounters(t *testing.T) {
	rows := []usagecatalog.Rollup{
		{Day: "2026-09-18", Source: "desktop", ModelRef: "deepseek/x", Provider: "deepseek",
			Total: 100, Requests: 2, Truncated: 3, CeilingHit: 2, GatewayCut: 1},
		{Day: "2026-09-18", Source: "desktop", ModelRef: "deepseek/y", Provider: "deepseek",
			Total: 50, Requests: 1, Truncated: 1, GatewayCut: 1},
	}
	day, _ := time.Parse(dayLayout, "2026-09-18")
	got := rangeStatsFromRollups(SourceFilter{From: day, To: day}, []string{"2026-09-18"}, rows)
	if got.Truncated != 4 || got.CeilingHit != 2 || got.GatewayCut != 2 {
		t.Fatalf("rollup aggregation lost the verdict: %+v", got)
	}
}

// The recorder's dispatcher is per-directory and shared, so a query right after
// a flush sees its own writes; a second recorder over the same directory must
// not double-count or reset anything.
func TestTruncationCountersSurviveRecorderRestart(t *testing.T) {
	dir := t.TempDir()
	first := NewRecorder(&spySink{}, dir, "desktop")
	first.Emit(event.Event{Kind: event.Usage, ModelRef: "deepseek/x",
		Usage: &provider.Usage{CompletionTokens: 4096, TotalTokens: 4096, MaxOutputTokens: 4096, FinishReason: "length"}})
	flushRecorder(t, first)

	second := NewRecorder(&spySink{}, dir, "cli")
	second.Emit(event.Event{Kind: event.Usage, ModelRef: "deepseek/x",
		Usage: &provider.Usage{CompletionTokens: 10, TotalTokens: 20, MaxOutputTokens: 4096, FinishReason: "stop"}})
	flushRecorder(t, second)

	from := time.Now().AddDate(0, 0, -1)
	all, err := NewWriter(dir).Query(SourceFilter{From: from, To: time.Now()})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if all.Truncated != 1 || all.CeilingHit != 1 {
		t.Fatalf("both recorders' rows must aggregate: %+v", all)
	}
	desktop, err := NewWriter(dir).Query(SourceFilter{Source: "desktop", From: from, To: time.Now()})
	if err != nil {
		t.Fatalf("query desktop: %v", err)
	}
	if desktop.Truncated != 1 || desktop.Requests != 1 {
		t.Fatalf("source filter should isolate the desktop row: %+v", desktop)
	}
}
