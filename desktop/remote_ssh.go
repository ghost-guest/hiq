package main

// remote_ssh.go — the SSH transport: dials through the netdev transport layer
// (auth incl. ssh-agent and ~/.ssh/config, system known_hosts + TOFU into a
// hiq-managed file with hard-fail on mismatch), provisions the Linux host
// binary by streaming it over an exec session's stdin, and runs `hiq host`
// over a plain session with piped stdio. Credentials live in the manager
// (in-memory) and the desktop secret store; RemoteRef persists only the
// non-secret parts (target, user, key path).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/netdev/transport"
	"github.com/zzycxz/hiq/internal/secret"
	"golang.org/x/crypto/ssh"
)

// sshCredentials is one SSH target's connection info. Secrets are held here at
// runtime and mirrored into the secret store; RemoteRef carries only
// Host(:Port)/User/KeyPath.
type sshCredentials struct {
	Host       string // host or ssh-config alias
	Port       string // "" => 22 / config
	User       string
	AuthMethod string // "password" | "privateKey"
	Password   string
	KeyPath    string
	Passphrase string
}

// sshCredsSecretKeys derives the secret-store keys for a target.
func sshCredsSecretKeys(host, port, user string) (passwordKey, passphraseKey string) {
	base := "HIQ_REMOTE_SSH_" + sshTargetSlug(host, port, user)
	return base + "_PASSWORD", base + "_PASSPHRASE"
}

func sshTargetSlug(host, port, user string) string {
	sanitize := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
			switch {
			case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		return b.String()
	}
	target := sanitize(host)
	if p := strings.TrimSpace(port); p != "" && p != "22" {
		target += "_" + sanitize(p)
	}
	if u := sanitize(user); u != "" {
		target += "_" + u
	}
	return target
}

// saveSSHCredentials persists credentials for reconnects: in-memory in the
// manager, and the secrets in the desktop's encrypted store (key path itself
// is not secret and lives in the RemoteRef).
func (m *remoteHostManager) saveSSHCredentials(creds *sshCredentials) {
	key := remoteRefKey(RemoteRef{Kind: "ssh", Target: sshTarget(creds.Host, creds.Port), User: creds.User})
	m.mu.Lock()
	if m.sshCreds == nil {
		m.sshCreds = make(map[string]*sshCredentials)
	}
	m.sshCreds[key] = creds
	m.mu.Unlock()

	if store := desktopSecretStore(); store != nil {
		passwordKey, passphraseKey := sshCredsSecretKeys(creds.Host, creds.Port, creds.User)
		if creds.Password != "" {
			_ = store.Set(passwordKey, creds.Password)
		}
		if creds.Passphrase != "" {
			_ = store.Set(passphraseKey, creds.Passphrase)
		}
	}
}

// loadSSHCredentials restores credentials for a target (manager cache first,
// secret store fallback), so a restarted app can still reconnect tabs whose
// key auth is password-based. Key-path auth needs no secret.
func (m *remoteHostManager) loadSSHCredentials(ref RemoteRef, keyPath string) *sshCredentials {
	key := remoteRefKey(ref)
	m.mu.Lock()
	creds := m.sshCreds[key]
	m.mu.Unlock()
	if creds != nil {
		return creds
	}
	host, port := splitSSHTarget(ref.Target)
	creds = &sshCredentials{Host: host, Port: port, User: ref.User, AuthMethod: "privateKey", KeyPath: keyPath}
	if store := desktopSecretStore(); store != nil {
		passwordKey, passphraseKey := sshCredsSecretKeys(host, port, ref.User)
		if v, ok, _ := store.Get(passwordKey); ok && v != "" {
			creds.AuthMethod = "password"
			creds.Password = v
		}
		if v, ok, _ := store.Get(passphraseKey); ok {
			creds.Passphrase = v
		}
	}
	return creds
}

func desktopSecretStore() *secret.Store {
	return secret.New(secret.DefaultPath())
}

func sshTarget(host, port string) string {
	if p := strings.TrimSpace(port); p != "" && p != "22" {
		return host + ":" + p
	}
	return host
}

func splitSSHTarget(target string) (host, port string) {
	if h, p, ok := splitHostPort(target); ok {
		return h, p
	}
	return target, ""
}

func splitHostPort(target string) (string, string, bool) {
	// IPv6 literals are rare for P1; handle the plain host:port case.
	if i := strings.LastIndex(target, ":"); i > 0 && i != len(target)-1 && strings.Count(target, ":") == 1 {
		return target[:i], target[i+1:], true
	}
	return "", "", false
}

type sshTransport struct {
	creds *sshCredentials
	// managedPath overrides the managed known_hosts location (tests).
	managedPath string
}

// sshProc owns the session and the underlying ssh client.
type sshProc struct {
	sess   *ssh.Session
	client *transport.Client
}

func (p *sshProc) Kill() error {
	_ = p.sess.Close()
	return p.client.Close()
}

func (p *sshProc) Wait() error {
	return p.sess.Wait()
}

// sshManagedPathOr is Dial's managed-path resolver (override-aware).
func sshManagedPathOr(t *sshTransport) string {
	if t.managedPath != "" {
		return t.managedPath
	}
	return remoteSSHKnownHostsPath()
}

// Dial resolves, authenticates, provisions the host binary, and spawns it.
func (t *sshTransport) Dial(ctx context.Context, ref RemoteRef) (io.Reader, io.Writer, remoteProcess, error) {
	creds := t.creds
	if creds == nil {
		return nil, nil, nil, fmt.Errorf("ssh: no credentials for %s", ref.Target)
	}

	resolved, auth, err := resolveSSHHost(creds)
	if err != nil {
		return nil, nil, nil, err
	}
	client, err := transport.New(transport.Options{
		Host: resolved,
		Auth: *auth,
		HostKeys: &transport.HostKeyPolicy{
			// System known_hosts + the hiq-managed file; unknown keys are
			// REJECTED (no silent TOFU) — the wizard confirms the fingerprint
			// first (SSHInspectHost/SSHTrustHost), which writes the managed
			// file, and this dial then accepts. A conflicting key fails hard.
			ManagedPath: sshManagedPathOr(t),
			Prompt: func(context.Context, transport.HostKeyQuestion) (bool, error) {
				return false, fmt.Errorf("host key not trusted yet — confirm the fingerprint in the remote-connect wizard")
			},
		},
		DialTimeout: 15 * time.Second,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssh: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("ssh: connect: %w", err)
	}
	sshClient, err := client.SSH()
	if err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("ssh: %w", err)
	}

	remoteBin, err := provisionSSHHost(ctx, sshClient)
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}

	sess, err := sshClient.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("ssh: session: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}
	sess.Stderr = os.Stderr
	if err := sess.Start(remoteBin + " host"); err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("ssh: start host: %w", err)
	}
	return stdout, stdin, &sshProc{sess: sess, client: client}, nil
}

func remoteSSHKnownHostsPath() string {
	dir := desktopConfigDir()
	return filepath.Join(dir, "remote-known-hosts")
}

// resolveSSHHost builds the transport's ResolvedHost + AuthOptions: an
// ssh-config alias expands through LoadUserSSHConfig; explicit host/port/user
// override whatever the config says.
func resolveSSHHost(creds *sshCredentials) (transport.ResolvedHost, *transport.AuthOptions, error) {
	target := strings.TrimSpace(creds.Host)
	if target == "" {
		return transport.ResolvedHost{}, nil, fmt.Errorf("ssh: host is required")
	}
	var src *transport.SSHConfigSource
	if srcErr := func() error {
		s, err := transport.LoadUserSSHConfig()
		if err != nil {
			return err
		}
		src = s
		return nil
	}(); srcErr != nil {
		src = nil // config is optional; direct targets still work
	}
	nameOrTarget := target
	if hostOnly, _, ok := splitHostPort(target); ok {
		nameOrTarget = hostOnly
	}
	resolved, err := transport.ResolveHost(nil, nameOrTarget, src)
	if err != nil {
		return transport.ResolvedHost{}, nil, fmt.Errorf("ssh: resolve host: %w", err)
	}
	if p := strings.TrimSpace(creds.Port); p != "" {
		if port, perr := strconv.Atoi(p); perr == nil {
			resolved.Port = port
		}
	}
	if u := strings.TrimSpace(creds.User); u != "" {
		resolved.User = u
	}
	if resolved.User == "" {
		resolved.User = "root"
	}
	if kp := strings.TrimSpace(creds.KeyPath); kp != "" {
		resolved.IdentityFile = kp
	}

	auth := &transport.AuthOptions{}
	if creds.AuthMethod == "password" && creds.Password != "" {
		pw := creds.Password
		auth.Password = func() (string, error) { return pw, nil }
		auth.DisableAgent = true
	} else if ph := creds.Passphrase; ph != "" {
		auth.Passphrase = func() (string, error) { return ph, nil }
	}
	return resolved, auth, nil
}

// provisionSSHHost ensures ~/.hiq/bin/hiq exists remotely with the
// same byte size as the local host binary, streaming the upload through an
// exec session's stdin (no SFTP dependency).
func provisionSSHHost(ctx context.Context, client *ssh.Client) (string, error) {
	machine, err := sshExecOutput(client, "uname -m")
	if err != nil {
		return "", fmt.Errorf("ssh: probe: %w", err)
	}
	arch := goarchFromUname(strings.TrimSpace(machine))
	local, err := localLinuxHostBinaryFor(arch)
	if err != nil {
		return "", err
	}
	localInfo, err := os.Stat(local)
	if err != nil {
		return "", err
	}
	remoteBin := "~/.hiq/bin/hiq"

	needUpload := true
	if out, err := sshExecOutput(client, "wc -c < ~/.hiq/bin/hiq 2>/dev/null"); err == nil {
		if size, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); perr == nil && size == localInfo.Size() {
			needUpload = false
		}
	}
	if needUpload {
		if _, err := sshExecOutput(client, "mkdir -p ~/.hiq/bin"); err != nil {
			return "", fmt.Errorf("ssh: mkdir: %w", err)
		}
		f, err := os.Open(local)
		if err != nil {
			return "", err
		}
		sess, err := client.NewSession()
		if err != nil {
			f.Close()
			return "", err
		}
		in, err := sess.StdinPipe()
		if err != nil {
			f.Close()
			sess.Close()
			return "", err
		}
		var errBuf bytes.Buffer
		sess.Stderr = &errBuf
		if err := sess.Start("sh -c 'cat > ~/.hiq/bin/hiq.tmp && chmod +x ~/.hiq/bin/hiq.tmp && mv ~/.hiq/bin/hiq.tmp ~/.hiq/bin/hiq'"); err != nil {
			f.Close()
			sess.Close()
			return "", fmt.Errorf("ssh: upload: %w", err)
		}
		if _, err := io.Copy(in, f); err != nil {
			f.Close()
			sess.Close()
			return "", fmt.Errorf("ssh: upload stream: %w", err)
		}
		f.Close()
		in.Close()
		if err := sess.Wait(); err != nil {
			return "", fmt.Errorf("ssh: upload: %v (%s)", err, strings.TrimSpace(errBuf.String()))
		}
	}
	return remoteBin, nil
}

// sshExecOutput runs one command line (the remote shell expands ~ etc.) and
// returns its stdout.
func sshExecOutput(client *ssh.Client, cmdline string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &out
	if err := sess.Run(cmdline); err != nil {
		return "", err
	}
	return out.String(), nil
}
