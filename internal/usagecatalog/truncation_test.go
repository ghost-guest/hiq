package usagecatalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The v2 migration adds the truncation counters, so both the schema upgrade and
// the reconcile → rollup path need to carry them. A fresh database goes through
// migration 1 then 2, which is exactly the path an existing install takes.

func TestTruncationCountersSurviveReconcileAndQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-18.jsonl")
	// One truncated call whose output filled its ceiling, one cut short by a
	// relay, one clean call.
	lines := `{"ts":"2026-09-18T10:00:00Z","model":"deepseek/x","source":"desktop","prompt":10,"completion":4096,"total":4106,"max_output":4096,"stop_reason":"length","truncated":true,"ceiling_hit":true}
{"ts":"2026-09-18T10:05:00Z","model":"deepseek/x","source":"desktop","prompt":10,"completion":700,"total":710,"max_output":8192,"stop_reason":"length","truncated":true,"gateway_cut":true}
{"ts":"2026-09-18T10:10:00Z","model":"deepseek/x","source":"desktop","prompt":10,"completion":20,"total":30,"max_output":4096,"stop_reason":"stop"}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(ctx, filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	if err := catalog.ReconcileFile(ctx, path, "2026-09-18"); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.Query(ctx, "2026-09-18", "2026-09-18", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one rollup row, got %d", len(rows))
	}
	row := rows[0]
	if row.Truncated != 2 || row.CeilingHit != 1 || row.GatewayCut != 1 {
		t.Fatalf("truncation counters lost in the projection: %+v", row)
	}
	if row.Total != 4106+710+30 {
		t.Errorf("token totals should be untouched, got %d", row.Total)
	}
}

func TestLegacyRowsProjectAsUnknownTruncation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-01-02.jsonl")
	// A row written before the counters existed.
	if err := os.WriteFile(path, []byte(`{"ts":"2026-01-02T10:00:00Z","model":"deepseek/x","source":"desktop","total":42}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(ctx, filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	if err := catalog.ReconcileFile(ctx, path, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	rows, err := catalog.Query(ctx, "2026-01-02", "2026-01-02", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Total != 42 {
		t.Fatalf("legacy row should still project: %#v", rows)
	}
	if rows[0].Truncated != 0 || rows[0].CeilingHit != 0 || rows[0].GatewayCut != 0 {
		t.Fatalf("a legacy row must project as unknown, not as a verdict: %+v", rows[0])
	}
}

func TestMigrationUpgradesAnExistingV1Database(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	// Build a v1 database, then close it and reopen: the second Open must apply
	// migration 2 in place rather than failing or recreating the file.
	catalog, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Force the recorded version back to 1 to simulate a pre-upgrade file.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.ExecContext(ctx, `ALTER TABLE usage_rollups DROP COLUMN truncated`); err != nil {
		t.Skipf("this driver cannot drop columns; migration idempotence is covered by the fresh-open tests: %v", err)
	}
	_ = reopened.Close(ctx)
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening a v1 database must migrate it, got %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close(context.Background()) })
	entry := Entry{Day: "2026-09-18", Source: "desktop", ModelRef: "deepseek/x", Provider: "deepseek", Total: 5, Requests: 1, Truncated: 1}
	if _, err := upgraded.db.ExecContext(ctx, `INSERT INTO usage_rollups(day,source,model_ref,provider,prompt,completion,reasoning,cache_hit,
        cache_miss,total,requests,turns,truncated,ceiling_hit,gateway_cut) VALUES(?,?,?,?,0,0,0,0,0,?,?,0,?,0,0)`,
		entry.Day, entry.Source, entry.ModelRef, entry.Provider, entry.Total, entry.Requests, entry.Truncated); err != nil {
		t.Fatalf("migrated schema should accept the truncation columns: %v", err)
	}
	if SchemaVersion != 2 {
		t.Fatalf("SchemaVersion should track the latest migration, got %d", SchemaVersion)
	}
}
