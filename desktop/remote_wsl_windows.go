//go:build windows

package main

// remote_wsl_windows.go — the P1 WSL transport: detects distros, provisions
// the Linux host binary into the distro (~/.hiq/bin/hiq), and spawns
// `hiq host` over wsl.exe pipes. The desktop's ConPTY terminal for remote
// tabs also runs wsl.exe directly (see PTYCreate), so no remote PTY protocol
// is needed.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// wslExe is the wsl.exe path (overridable in tests).
var wslExe = "wsl.exe"

type wslTransport struct{}

func newWSLTransport() remoteTransport { return &wslTransport{} }

// wslDistro is one installed distro.
type wslDistro struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Version int    `json:"version"`
	Default bool   `json:"default"`
}

// ListWSLDistros enumerates installed distros via `wsl -l -v`. Returns an
// empty list when WSL itself is absent (the wizard shows guidance instead).
func (a *App) ListWSLDistros() []wslDistro {
	out, err := runWSLOutput(nil, "-l", "-v")
	if err != nil {
		return []wslDistro{}
	}
	return parseWSLList(out)
}

// runWSLOutput runs wsl.exe and decodes its output: wsl.exe emits UTF-16LE
// when stdout is a pipe, ASCII otherwise — sniff and decode both.
func runWSLOutput(extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command(wslExe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	raw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return decodeWSLOutput(raw), nil
}

// decodeWSLOutput sniffs UTF-16LE (NUL byte in every other position) and
// falls back to raw bytes.
func decodeWSLOutput(raw []byte) string {
	utf16ish := len(raw) >= 2 && raw[1] == 0
	if !utf16ish {
		if s := strings.TrimSpace(string(raw)); s != "" || len(raw) == 0 {
			// Plain ASCII/UTF-8 already.
			if !bytes.ContainsRune(raw, 0) {
				return string(raw)
			}
		}
	}
	u16 := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u16 = append(u16, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(u16))
}

// parseWSLList parses `wsl -l -v` output lines like:
//
//	* Ubuntu    Running    2
//	  Debian    Stopped    1
func parseWSLList(out string) []wslDistro {
	distros := []wslDistro{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r\x00")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "NAME") {
			continue
		}
		def := strings.HasPrefix(trimmed, "*")
		fields := strings.Fields(strings.TrimPrefix(trimmed, "*"))
		if len(fields) < 2 {
			continue
		}
		d := wslDistro{Name: fields[0], State: fields[1], Default: def}
		if len(fields) >= 3 {
			fmt.Sscanf(fields[2], "%d", &d.Version)
		}
		distros = append(distros, d)
	}
	return distros
}

// wslProc adapts *exec.Cmd to remoteProcess.
type wslProc struct{ cmd *exec.Cmd }

func (p *wslProc) Kill() error { return p.cmd.Process.Kill() }
func (p *wslProc) Wait() error { return p.cmd.Wait() }

// Dial provisions and spawns the host for a WSL ref.
func (t *wslTransport) Dial(ctx context.Context, ref RemoteRef) (io.Reader, io.Writer, remoteProcess, error) {
	distro := ref.Target
	if strings.TrimSpace(distro) == "" {
		return nil, nil, nil, fmt.Errorf("wsl: distro name is required")
	}
	userArgs := []string{}
	if u := strings.TrimSpace(ref.User); u != "" {
		userArgs = []string{"-u", u}
	}
	// Wake the distro (a stopped distro makes the UNC share unreachable).
	_, _ = runWSLOutput(nil, append([]string{"-d", distro, "--exec", "true"}, userArgs...)...)

	home, err := wslHome(distro, ref.User)
	if err != nil {
		return nil, nil, nil, err
	}
	binPath, err := provisionWSLHost(distro, ref.User, home)
	if err != nil {
		return nil, nil, nil, err
	}

	args := append([]string{"-d", distro}, userArgs...)
	args = append(args, "--exec", binPath, "host")
	cmd := exec.Command(wslExe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("wsl: start host: %w", err)
	}
	// The JSON-RPC client writes TO the host's stdin and reads FROM its
	// stdout: the link reads stdout and writes stdin.
	return stdout, stdin, &wslProc{cmd: cmd}, nil
}

// wslHome resolves the distro-side home directory for the selected user.
func wslHome(distro, user string) (string, error) {
	args := []string{"-d", distro}
	if u := strings.TrimSpace(user); u != "" {
		args = append(args, "-u", u)
	}
	args = append(args, "--exec", "sh", "-c", "printf %s \"$HOME\"")
	out, err := runWSLOutput(nil, args...)
	if err != nil {
		return "", fmt.Errorf("wsl: resolve home: %w", err)
	}
	home := strings.TrimSpace(decodeWSLOutput([]byte(out)))
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("wsl: unexpected home %q", home)
	}
	return home, nil
}

// provisionWSLHost copies the desktop-side Linux host binary into the distro
// and marks it executable. The binary is looked up in the host cache
// (%LOCALAPPDATA%\hiq\hosts) and beside the desktop exe (dev builds); a
// missing binary is a clear, actionable error.
func provisionWSLHost(distro, user, home string) (string, error) {
	local, err := localLinuxHostBinary()
	if err != nil {
		return "", err
	}
	remoteBin := home + "/.hiq/bin/hiq"
	unc := wslDistroUNC(distro, remoteBin)
	if err := os.MkdirAll(filepath.Dir(unc), 0o755); err != nil {
		return "", fmt.Errorf("wsl: create bin dir: %w", err)
	}
	needCopy := true
	if dst, err := os.ReadFile(unc); err == nil {
		if src, err := os.ReadFile(local); err == nil && bytes.Equal(dst, src) {
			needCopy = false
		}
	}
	if needCopy {
		src, err := os.ReadFile(local)
		if err != nil {
			return "", fmt.Errorf("wsl: read host binary: %w", err)
		}
		if err := os.WriteFile(unc, src, 0o755); err != nil {
			return "", fmt.Errorf("wsl: copy host binary: %w", err)
		}
	}
	// chmod via wsl.exe (UNC writes don't set the exec bit).
	chmod := []string{"-d", distro}
	if u := strings.TrimSpace(user); u != "" {
		chmod = append(chmod, "-u", u)
	}
	chmod = append(chmod, "--exec", "chmod", "+x", remoteBin)
	if out, err := runWSLOutput(nil, chmod...); err != nil {
		return "", fmt.Errorf("wsl: chmod host: %v (%s)", err, strings.TrimSpace(out))
	}
	return remoteBin, nil
}

// localLinuxHostBinary finds the prebuilt Linux CLI to install into the distro.
// Release/desktop builds ship it in the hosts cache; dev builds look beside
// the running exe and in the repo tree.
func localLinuxHostBinary() (string, error) {
	arch := "amd64"
	if out, err := runWSLOutput(nil, "--exec", "uname", "-m"); err == nil {
		switch strings.TrimSpace(strings.TrimRight(out, "\r\x00")) {
		case "aarch64", "arm64":
			arch = "arm64"
		}
	}
	var candidates []string
	if cache, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(cache, "hiq", "hosts", "hiq-linux-"+arch))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "hosts", "hiq-linux-"+arch),
			filepath.Join(filepath.Dir(exe), "..", "..", "hosts", "hiq-linux-"+arch),
		)
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Size() > 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("Linux host binary not found (looked in %%LOCALAPPDATA%%\\hiq\\hosts and beside the desktop exe). Run scripts/build-hosts.sh (or `GOOS=linux GOARCH=%s go build -o <cache>/hiq/hosts/hiq-linux-%s ./cmd/hiq`) and retry", arch, arch)
}

// wslHomeForProbe resolves the distro home for the wizard's default dir.
func wslHomeForProbe(distro, user string) (string, error) {
	return wslHome(distro, user)
}
