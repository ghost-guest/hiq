package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	skillassets "github.com/zzycxz/hiq/internal/assets"

	"log/slog"
)

// ensurePPTAutoDeps preinstalls the ppt-auto skill's Python dependencies
// (python-pptx / Pillow / lxml / cairosvg) in the background at desktop
// startup, so the FIRST ppt generation doesn't pay the dependency-install
// cliff mid-task (the skill's own setup instructions would otherwise run
// inside a 2-minute bash budget during Step 0).
//
// Guarded by a .deps-installed marker inside the released skill dir: success
// writes it and we never run again; failure leaves it absent so the next
// launch retries. Best-effort by design — any error only logs.
func ensurePPTAutoDeps() {
	dir, err := skillassets.PPTAutoSkillDir()
	if err != nil || dir == "" {
		return
	}
	marker := filepath.Join(dir, ".deps-installed")
	if _, err := os.Stat(marker); err == nil {
		return
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "setup_python.bat")
		if _, err := os.Stat(script); err != nil {
			return
		}
		cmd = exec.Command("cmd", "/c", script)
	} else {
		script := filepath.Join(dir, "setup_python.sh")
		if _, err := os.Stat(script); err != nil {
			return
		}
		cmd = exec.Command("bash", script)
	}
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = nil, nil

	start := time.Now()
	if err := cmd.Run(); err != nil {
		// Most common cause: no Python on PATH — the script prints its own
		// guidance; we just log and retry next launch (no marker written).
		slog.Warn("ppt-auto: background dependency preinstall failed (will retry next launch)", "err", err)
		return
	}
	if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		slog.Warn("ppt-auto: failed to write deps marker", "err", err)
	}
	slog.Info("ppt-auto: python dependencies preinstalled", "took", time.Since(start).Round(time.Millisecond))
}
