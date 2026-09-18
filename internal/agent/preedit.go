package agent

import (
	"sync"

	"github.com/zzycxz/hiq/internal/diff"
)

// SharedPreEditHook lets the controller attach its checkpoint snapshotter to
// agents that are constructed before the controller exists. boot creates one
// instance and threads it into every sub-agent spawn site (the task tool and
// the run_skill runner); once control.New has bound the checkpoint store, the
// concrete function is installed via Set. Until then Fire is a no-op, so
// headless builds (no controller checkpoint store) behave exactly as before.
type SharedPreEditHook struct {
	mu sync.Mutex
	fn func(diff.Change)
}

func NewSharedPreEditHook() *SharedPreEditHook { return &SharedPreEditHook{} }

// Set installs (or replaces, on session rebind) the snapshot function.
func (h *SharedPreEditHook) Set(fn func(diff.Change)) {
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

// Fire is the hook handed to agent.Options.PreEditHook. It must be safe to
// call concurrently — writer tools dispatch in parallel goroutines.
func (h *SharedPreEditHook) Fire(ch diff.Change) {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn != nil {
		fn(ch)
	}
}

// SharedPostEditHook is the post-edit twin of SharedPreEditHook: it carries the
// checkpoint store's post-edit hash recorder to sub-agents constructed before
// the controller exists. Until Set installs the function, Fire is a no-op.
type SharedPostEditHook struct {
	mu sync.Mutex
	fn func(string)
}

func NewSharedPostEditHook() *SharedPostEditHook { return &SharedPostEditHook{} }

func (h *SharedPostEditHook) Set(fn func(string)) {
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

// Fire is the hook handed to agent.Options.PostEditHook; safe for concurrent use.
func (h *SharedPostEditHook) Fire(path string) {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn != nil {
		fn(path)
	}
}
