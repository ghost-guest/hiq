package main

import (
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/provider"
	"github.com/zzycxz/fairpeer/internal/stats"
)

// isolateStateHome points every directory the config layer derives (user dir →
// stats/sessions/archive) at throwaway temp dirs, so a recording test never
// touches the real developer profile.
func isolateStateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", robustTempDir(t))
	t.Setenv("USERPROFILE", robustTempDir(t))
	t.Setenv("XDG_CONFIG_HOME", robustTempDir(t))
	t.Setenv("AppData", robustTempDir(t))
}

// TestResolveStatsRange covers the six branches of resolveStatsRange: the four
// preset day ranges, custom valid/invalid dates, and the unknown-range default.
// resolveStatsRange anchors to time.Now(), so assertions are relative — the
// returned day (local calendar date) must equal the expected offset from today.
func TestResolveStatsRange(t *testing.T) {
	now := time.Now()
	today := func(offsetDays int) time.Time {
		y, m, d := now.AddDate(0, 0, offsetDays).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}

	tests := []struct {
		name    string
		req     UsageStatsRequest
		want    [2]time.Time // [from, to] as local day starts
		wantErr string       // substring of the error, "" for success
	}{
		{
			name: "preset 7 days",
			req:  UsageStatsRequest{Range: "7"},
			want: [2]time.Time{today(-6), today(0)},
		},
		{
			name: "preset 14 days",
			req:  UsageStatsRequest{Range: "14"},
			want: [2]time.Time{today(-13), today(0)},
		},
		{
			name: "preset 30 days",
			req:  UsageStatsRequest{Range: "30"},
			want: [2]time.Time{today(-29), today(0)},
		},
		{
			name: "preset 90 days",
			req:  UsageStatsRequest{Range: "90"},
			want: [2]time.Time{today(-89), today(0)},
		},
		{
			name: "custom valid dates",
			req:  UsageStatsRequest{Range: "custom", From: "2026-07-01", To: "2026-07-31"},
			want: [2]time.Time{time.Date(2026, 7, 1, 0, 0, 0, 0, now.Location()), time.Date(2026, 7, 31, 23, 59, 59, 0, now.Location())},
		},
		{
			name:    "custom missing from",
			req:     UsageStatsRequest{Range: "custom", To: "2026-07-31"},
			wantErr: "needs valid from/to dates",
		},
		{
			name:    "custom malformed to",
			req:     UsageStatsRequest{Range: "custom", From: "2026-07-01", To: "not-a-date"},
			wantErr: "needs valid from/to dates",
		},
		{
			name:    "custom reversed dates",
			req:     UsageStatsRequest{Range: "custom", From: "2026-07-31", To: "2026-07-01"},
			wantErr: "must not be after",
		},
		{
			name:    "custom future to date",
			req:     UsageStatsRequest{Range: "custom", From: today(0).Format(statsDateLayout), To: today(1).Format(statsDateLayout)},
			wantErr: "must not be in the future",
		},
		{
			name: "custom maximum span",
			req: UsageStatsRequest{
				Range: "custom",
				From:  today(-(maxStatsCustomRangeDays - 1)).Format(statsDateLayout),
				To:    today(0).Format(statsDateLayout),
			},
			want: [2]time.Time{today(-(maxStatsCustomRangeDays - 1)), today(0)},
		},
		{
			name: "custom over maximum span",
			req: UsageStatsRequest{
				Range: "custom",
				From:  today(-maxStatsCustomRangeDays).Format(statsDateLayout),
				To:    today(0).Format(statsDateLayout),
			},
			wantErr: "cannot exceed",
		},
		{
			name: "empty range defaults to 7 days",
			req:  UsageStatsRequest{},
			want: [2]time.Time{today(-6), today(0)},
		},
		{
			name: "unknown range defaults to 7 days",
			req:  UsageStatsRequest{Range: "365"},
			want: [2]time.Time{today(-6), today(0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, err := resolveStatsRange(tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Compare local calendar-day identity: the helper builds day starts,
			// resolveStatsRange returns from at 00:00:00 and to at 23:59:59.
			dayOf := func(tm time.Time) string { return tm.Format(statsDateLayout) }
			if dayOf(from) != dayOf(tt.want[0]) {
				t.Fatalf("from: want %s, got %s", dayOf(tt.want[0]), dayOf(from))
			}
			if dayOf(to) != dayOf(tt.want[1]) {
				t.Fatalf("to: want %s, got %s", dayOf(tt.want[1]), dayOf(to))
			}
		})
	}
}

// TestResolveStatsRangeToIsEndOfDay pins the "to" boundary to 23:59:59 so a
// future change cannot silently shrink the range to a single moment.
func TestResolveStatsRangeToIsEndOfDay(t *testing.T) {
	_, to, err := resolveStatsRange(UsageStatsRequest{Range: "7"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if to.Hour() != 23 || to.Minute() != 59 || to.Second() != 59 {
		t.Fatalf("to must be end-of-day 23:59:59, got %v", to)
	}
}

// TestUsageStatsFlushesPendingRecorderWrites is the end-to-end guard for the
// whole feature: a Usage event emitted into the recorder (which writes
// asynchronously) must be visible to App.UsageStats immediately, and must be
// filtered by its source label. If the short flush window in UsageStats ever
// goes away, the panel silently reports zero for the last few minutes of work —
// exactly the numbers a user opens the panel to see.
func TestUsageStatsFlushesPendingRecorderWrites(t *testing.T) {
	isolateStateHome(t)
	recorder := stats.NewRecorder(event.Discard, config.StatsDir(), "desktop")
	recorder.Emit(event.Event{
		Kind: event.Usage, ModelRef: "deepseek/model",
		Usage: &provider.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	})

	result, err := (&App{}).UsageStats(UsageStatsRequest{Range: "7", Source: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tokens != 5 || result.Requests != 1 {
		t.Fatalf("usage stats = tokens %d requests %d, want 5/1", result.Tokens, result.Requests)
	}

	// A different entry point must not pick up this run's rows.
	other, err := (&App{}).UsageStats(UsageStatsRequest{Range: "7", Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Tokens != 0 || other.Requests != 0 {
		t.Fatalf("cli-filtered usage = tokens %d requests %d, want 0/0", other.Tokens, other.Requests)
	}
}
