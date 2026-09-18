package netdev

// dockerapi.go — kind=docker 的只读 Docker Engine 客户端（NETDEV_SPEC_V2 §2.2）。
// 极简 HTTP + GET 白名单：不引 docker SDK（省一棵依赖树），且**不存在**
// POST/DELETE 的客户端代码路径——结构性只读在代码层面成立，不是配置约束。
// socket 三态：npipe（Windows 本地 Docker Desktop）/ unix / tcp（仅清单主机）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/config"
)

const dockerBodyCap = 256 * 1024

// dockerIDRe constrains container IDs/names used in paths — one plain token,
// no slashes or metacharacters (path-injection guard).
var dockerIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// dockerTransport builds the HTTP transport for the configured socket.
func dockerTransport(socket string) (*http.Transport, error) {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		if runtime.GOOS == "windows" {
			socket = "npipe:////./pipe/docker_engine"
		} else {
			socket = "unix:///var/run/docker.sock"
		}
	}
	switch {
	case strings.HasPrefix(socket, "npipe://"):
		return newNpipeTransport(strings.TrimPrefix(socket, "npipe://"))
	case strings.HasPrefix(socket, "unix://"):
		path := strings.TrimPrefix(socket, "unix://")
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}, nil
	case strings.HasPrefix(socket, "tcp://"):
		addr := strings.TrimPrefix(socket, "tcp://")
		return &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
		}, nil
	}
	return nil, fmt.Errorf("docker socket must be npipe:// | unix:// | tcp://, got %q", socket)
}

// DockerGet answers ONE whitelisted GET against the device's Docker Engine.
// what ∈ ping | version | info | ps | inspect | logs | images. Sealed by
// sealAPIGet (guardrails + live + audit + redaction).
func (m *Manager) DockerGet(ctx context.Context, deviceName, what, arg string, tailN int) (string, error) {
	d, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return "", fmt.Errorf("device %q is not in the inventory (add it in 运维设置)", deviceName)
	}
	if d.Kind != "docker" || d.Docker == nil {
		return "", fmt.Errorf("device %q is not a kind=docker target", deviceName)
	}
	// tcp:// endpoints must match the registered device address — the socket
	// field is human-configured, but a mismatch is still refused rather than
	// dialed (defense in depth against config mistakes).
	socket := strings.TrimSpace(d.Docker.Socket)
	if strings.HasPrefix(socket, "tcp://") && strings.TrimSpace(d.Address) != "" {
		host := strings.TrimPrefix(socket, "tcp://")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !strings.EqualFold(host, strings.TrimSpace(d.Address)) {
			return "", fmt.Errorf("docker socket tcp host %q does not match the device address %q", host, d.Address)
		}
	}
	label := "docker " + what
	if arg != "" {
		label += " " + arg
	}
	return m.sealAPIGet(deviceName, label, func() (string, error) {
		tr, err := dockerTransport(d.Docker.Socket)
		if err != nil {
			return "", err
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}

		var path string
		switch what {
		case "ping":
			path = "/_ping"
		case "version":
			path = "/version"
		case "info":
			path = "/info"
		case "ps":
			path = "/containers/json"
		case "images":
			path = "/images/json"
		case "inspect", "logs":
			if !dockerIDRe.MatchString(arg) {
				return "", fmt.Errorf("invalid container id/name %q (one plain token)", arg)
			}
			if what == "inspect" {
				path = "/containers/" + arg + "/json"
			} else {
				if tailN <= 0 || tailN > 1000 {
					tailN = 100
				}
				path = fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d", arg, tailN)
			}
		default:
			return "", errors.New("what must be ping|version|info|ps|inspect|logs|images")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
		if err != nil {
			return "", err
		}
		res, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, dockerBodyCap))
		if err != nil {
			return "", err
		}
		if res.StatusCode != http.StatusOK {
			return string(body), fmt.Errorf("docker API %s → HTTP %d", path, res.StatusCode)
		}
		return string(body), nil
	})
}

// kindDocker quick check for the tools layer.
func kindDocker(d config.NetDevDevice) bool { return d.Kind == "docker" && d.Docker != nil }
