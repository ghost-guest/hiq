package main

// remote_docker.go — the Docker transport: detects running containers, copies
// the Linux host binary in via `docker cp`, and spawns `hiq host` with
// `docker exec -i`. Same stdio JSON-RPC pattern as WSL, so one remoteHostLink
// serves both.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// dockerExe is overridable in tests.
var dockerExe = "docker"

type dockerTransport struct{}

// dockerContainer is one running container (docker ps --format '{{json .}}').
type dockerContainer struct {
	ID     string `json:"ID"`
	Image  string `json:"Image"`
	Names  string `json:"Names"` // comma-separated
	State  string `json:"State"`
	Status string `json:"Status"`
}

// ListDockerContainers enumerates running containers for the wizard. Returns
// an empty list plus the error message when the engine is unreachable (the
// wizard surfaces guidance instead of a dead dropdown).
func (a *App) ListDockerContainers() ([]dockerContainer, error) {
	out, err := dockerOutput("ps", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	return parseDockerPS(out), nil
}

func dockerOutput(args ...string) (string, error) {
	cmd := exec.Command(dockerExe, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// parseDockerPS parses the NDJSON lines `docker ps --format '{{json .}}'` emits.
func parseDockerPS(out string) []dockerContainer {
	containers := []dockerContainer{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c dockerContainer
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		if c.Names == "" {
			c.Names = c.ID
		}
		containers = append(containers, c)
	}
	return containers
}

// dockerProc adapts *exec.Cmd to remoteProcess.
type dockerProc struct{ cmd *exec.Cmd }

func (p *dockerProc) Kill() error { return p.cmd.Process.Kill() }
func (p *dockerProc) Wait() error { return p.cmd.Wait() }

// Dial provisions and spawns the host inside ref.Target (container name/id).
func (t *dockerTransport) Dial(ctx context.Context, ref RemoteRef) (io.Reader, io.Writer, remoteProcess, error) {
	container := strings.TrimSpace(ref.Target)
	if container == "" {
		return nil, nil, nil, fmt.Errorf("docker: container name is required")
	}

	machine, err := dockerExecOutput(container, "uname", "-m")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("docker: container not reachable: %w", err)
	}
	arch := goarchFromUname(strings.TrimSpace(machine))
	local, err := localLinuxHostBinaryFor(arch)
	if err != nil {
		return nil, nil, nil, err
	}
	home, err := dockerExecOutput(container, "sh", "-c", `printf %s "$HOME"`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("docker: resolve home: %w", err)
	}
	home = strings.TrimSpace(home)
	if !strings.HasPrefix(home, "/") {
		return nil, nil, nil, fmt.Errorf("docker: unexpected home %q", home)
	}
	remoteBin := home + "/.hiq/bin/hiq"

	if _, err := dockerOutput("exec", container, "mkdir", "-p", home+"/.hiq/bin"); err != nil {
		return nil, nil, nil, fmt.Errorf("docker: mkdir bin: %w", err)
	}
	// docker cp overwrites content; cheap enough to always copy (byte-compare
	// would need an extra exec round-trip of the whole file hash).
	if _, err := dockerOutput("cp", local, container+":"+remoteBin); err != nil {
		return nil, nil, nil, fmt.Errorf("docker: copy host binary: %w", err)
	}
	if _, err := dockerOutput("exec", container, "chmod", "+x", remoteBin); err != nil {
		return nil, nil, nil, fmt.Errorf("docker: chmod host: %w", err)
	}

	cmd := exec.Command(dockerExe, "exec", "-i", container, remoteBin, "host")
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
		return nil, nil, nil, fmt.Errorf("docker: start host: %w", err)
	}
	return stdout, stdin, &dockerProc{cmd: cmd}, nil
}

// dockerExecOutput runs one command inside the container and returns its stdout.
func dockerExecOutput(container string, args ...string) (string, error) {
	out, err := dockerOutput(append([]string{"exec", container}, args...)...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// dockerHomeForProbe resolves the container's default-user home for the wizard.
func dockerHomeForProbe(container string) (string, error) {
	home, err := dockerExecOutput(container, "sh", "-c", `printf %s "$HOME"`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(home), nil
}
