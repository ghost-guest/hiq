// worktree.go — gap analysis §5: sub-agent git worktree isolation.
// Each sub-agent that will write files gets its own git worktree, so
// parallel sub-agents never conflict on the same file. After the sub-agent
// finishes, its changes are merged back as a diff (not a git merge — the
// parent's session context drives whether to apply them).
//
// Design:
// - Only create a worktree when the sub-agent has writer tools (read-only
//   sub-agents share the parent's workspace freely).
// - The worktree lives under `.hiq/worktrees/<call-id>/` and is removed
//   after the diff is extracted.
// - The diff is returned to the model as a unified diff it can choose to
//   apply via apply_patch, keeping the approval flow intact.
package agent

import (
	"fmt"

	"github.com/zzycxz/hiq/internal/tool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeIsolation manages one sub-agent's isolated working copy.
type WorktreeIsolation struct {
	// Root is the original workspace root.
	Root string
	// Path is the worktree mount point.
	Path string
	// Branch is the git branch created for this worktree.
	Branch string
	// active reports whether a worktree was actually created (false when the
	// workspace is not a git repo, or the sub-agent is read-only).
	active bool
}

// CreateWorktree sets up an isolated worktree for a sub-agent call.
// Returns a no-op isolation when root is not a git repo or isGit is false.
func CreateWorktree(root, callID string, isGit bool) *WorktreeIsolation {
	wt := &WorktreeIsolation{Root: root}
	if !isGit || root == "" || callID == "" {
		return wt
	}

	// Verify git is available and root is a work tree.
	if !isGitRepo(root) {
		return wt
	}

	// Determine the default branch to base from.
	baseBranch := gitOutput(root, "rev-parse", "--abbrev-ref", "HEAD")
	if baseBranch == "" {
		return wt
	}

	// Worktree path: .hiq/worktrees/<call-id>
	wtPath := filepath.Join(root, ".hiq", "worktrees", sanitizeID(callID))
	wt.Branch = fmt.Sprintf("hiq/subagent-%s", sanitizeID(callID))

	// Create the worktree.
	if err := gitRun(root, "worktree", "add", "-b", wt.Branch, wtPath, baseBranch); err != nil {
		return wt
	}

	wt.Path = wtPath
	wt.active = true
	return wt
}

// Active reports whether isolation is in effect.
func (w *WorktreeIsolation) Active() bool { return w.active }

// WorkDir returns the directory the sub-agent should treat as its workspace
// (the worktree when active, the original root otherwise).
func (w *WorktreeIsolation) WorkDir() string {
	if w.active {
		return w.Path
	}
	return w.Root
}

// Diff returns the sub-agent's changes as a unified diff against the base.
// Empty string when no changes.
func (w *WorktreeIsolation) Diff() string {
	if !w.active {
		return ""
	}
	return gitOutput(w.Path, "diff", "HEAD")
}

// Cleanup removes the worktree and its branch. Safe to call multiple times.
func (w *WorktreeIsolation) Cleanup() {
	if !w.active {
		return
	}
	w.active = false
	// Remove the worktree.
	_ = gitRun(w.Root, "worktree", "remove", "--force", w.Path)
	// Delete the branch (changes are already extracted as a diff).
	if w.Branch != "" {
		_ = gitRun(w.Root, "branch", "-D", w.Branch)
	}
}

func isGitRepo(root string) bool {
	gitDir := filepath.Join(root, ".git")
	_, err := os.Stat(gitDir)
	return err == nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func sanitizeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, id)
}

// subRegHasWriter reports whether the sub-agent's tool set includes any
// file-writing tool. Read-only sub-agents share the parent workspace safely.
func subRegHasWriter(reg *tool.Registry) bool {
	for _, name := range reg.Names() {
		if t, ok := reg.Get(name); ok && !t.ReadOnly() {
			return true
		}
	}
	return false
}
