// subagent_approver.go — upgrade spec 3-10: bridging sub-agent Ask decisions
// to the main agent's approval card when an interactive approver exists.
//
// The old behaviour: sub-agents (task/run_skill) ran behind a headless gate
// whose Approver was nil, so Ask-level writer tools (doc_write, browser_*)
// silently passed — while the same call in the main loop would prompt the
// user. External-risk ops (email_send, MCP) were denied outright. That split
// was defensible for unattended runs but confusing interactively: the user
// approved "write the report" and the sub-agent could then write any file
// without a card.
//
// The bridge: when the controller has an interactive approver (desktop/TUI),
// sub-agent Ask-level calls route through it with a "sub-agent:" prefix so
// the approval card tells the user WHO is asking. The controller installs the
// bridge in EnableInteractiveApproval; headless runs keep the old semantics.
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/zzycxz/hiq/internal/permission"
)

// SubagentApprover is the installable bridge. nil Approver = headless.
type SubagentApprover struct {
	mu    sync.RWMutex
	inner permission.Approver
}

var subagentApprover = &SubagentApprover{}

// SetSubagentApprover installs (or clears) the bridge. Called by the
// controller's EnableInteractiveApproval; headless setups never call it and
// the gate keeps its nil-approver semantics.
func SetSubagentApprover(a permission.Approver) {
	subagentApprover.mu.Lock()
	subagentApprover.inner = a
	subagentApprover.mu.Unlock()
}

// SubagentApproverActive reports whether an interactive bridge is installed.
func SubagentApproverActive() bool {
	subagentApprover.mu.RLock()
	defer subagentApprover.mu.RUnlock()
	return subagentApprover.inner != nil
}

// Approve satisfies permission.Approver. With no bridge installed it allows
// everything (the old headless behaviour — external-risk denies happen in the
// Gate before reaching here). With a bridge it prefixes the subject so the
// user sees the call comes from a sub-agent, not the main conversation.
func (s *SubagentApprover) Approve(ctx context.Context, toolName, subject string, args json.RawMessage) (bool, bool, error) {
	s.mu.RLock()
	inner := s.inner
	s.mu.RUnlock()
	if inner == nil {
		return true, false, nil // headless: preserve autonomy
	}
	prefixed := subject
	if prefixed != "" {
		prefixed = fmt.Sprintf("[sub-agent] %s", subject)
	} else {
		prefixed = "[sub-agent]"
	}
	_ = strings.TrimSpace(prefixed)
	return inner.Approve(ctx, toolName, prefixed, args)
}
