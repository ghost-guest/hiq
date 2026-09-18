package main

import (
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/memory"
)

// TestMemoryFactViewMapsFields verifies the DTO mapper carries core fields
// through to the frontend.
func TestMemoryFactViewMapsFields(t *testing.T) {
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	src := memory.Memory{
		Name:      "lives-in-beijing",
		Body:      "User lives in Beijing.",
		Type:      memory.TypeUser,
		CreatedAt: created,
	}

	got := memoryFactView(src)

	if got.Name != "lives-in-beijing" {
		t.Errorf("Name = %q, want lives-in-beijing", got.Name)
	}
	if got.Body != "User lives in Beijing." {
		t.Errorf("Body = %q", got.Body)
	}
	if got.CreatedAt != created.Format(time.RFC3339) {
		t.Errorf("CreatedAt = %q, want %q", got.CreatedAt, created.Format(time.RFC3339))
	}
}
