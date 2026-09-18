package main

// remote_ssh_trust.go — first-connect host-key confirmation for SSH. The
// transport itself rejects unknown keys (no silent TOFU); the wizard surfaces
// the fingerprint via SSHInspectHost, the user confirms, and SSHTrustHost
// writes the key into the hiq-managed known_hosts so the supervised dial
// accepts it afterwards. Conflicting keys always fail hard inside the
// known_hosts callback.

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHHostInfo is what the wizard's fingerprint step shows.
type SSHHostInfo struct {
	Fingerprint string `json:"fingerprint"` // ssh.FingerprintSHA256 form, "SHA256:…"
	Trusted     bool   `json:"trusted"`     // already in system or managed known_hosts
}

// sshFetchHostKey connects far enough to capture the server's public key (no
// auth) and aborts the handshake.
func sshFetchHostKey(host, port string) (ssh.PublicKey, error) {
	if port == "" {
		port = "22"
	}
	addr := net.JoinHostPort(host, port)
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var captured ssh.PublicKey
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	cfg := &ssh.ClientConfig{
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return fmt.Errorf("key captured")
		},
	}
	_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
	if captured == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("host did not present a key")
	}
	return captured, nil
}

// sshKnownHostsCallback builds a verifier over the system files plus the
// hiq-managed file (mirrors the transport's HostKeyPolicy sources).
func sshKnownHostsCallback(managed string) (ssh.HostKeyCallback, error) {
	if managed != "" {
		if err := os.MkdirAll(filepath.Dir(managed), 0o700); err == nil {
			if _, statErr := os.Stat(managed); os.IsNotExist(statErr) {
				_ = os.WriteFile(managed, nil, 0o600)
			}
		}
	}
	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		for _, f := range []string{filepath.Join(home, ".ssh", "known_hosts"), filepath.Join(home, ".ssh", "known_hosts2")} {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
	}
	if managed != "" {
		files = append(files, managed)
	}
	if len(files) == 0 {
		// knownhosts.New needs at least one file; an empty temp file works.
		tmp, err := os.CreateTemp("", "hiq-empty-knownhosts-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		tmp.Close()
		files = append(files, tmp.Name())
	}
	return knownhosts.New(files...)
}

// SSHInspectHost fetches the host key fingerprint and reports whether the key
// is already trusted (system known_hosts or the hiq-managed file).
func (a *App) SSHInspectHost(host, port, user string) (SSHHostInfo, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return SSHHostInfo{}, fmt.Errorf("ssh host is required")
	}
	key, err := sshFetchHostKey(host, strings.TrimSpace(port))
	if err != nil {
		return SSHHostInfo{}, fmt.Errorf("ssh: reach host: %w", err)
	}
	cb, err := sshKnownHostsCallback(remoteSSHKnownHostsPath())
	if err != nil {
		return SSHHostInfo{}, err
	}
	trusted := cb(net.JoinHostPort(host, port), nil, key) == nil
	return SSHHostInfo{Fingerprint: ssh.FingerprintSHA256(key), Trusted: trusted}, nil
}

// SSHTrustHost records the currently-presented host key as trusted (called
// after the user confirms the fingerprint in the wizard). A key conflict still
// fails later dials: this appends only what the host presents right now.
func (a *App) SSHTrustHost(host, port string) error {
	return sshTrustHostKey(host, port, remoteSSHKnownHostsPath())
}

// sshTrustHostKey is SSHTrustHost's testable core.
func sshTrustHostKey(host, port, managed string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("ssh host is required")
	}
	port = strings.TrimSpace(port)
	key, err := sshFetchHostKey(host, port)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(managed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	marker := knownhosts.Normalize(net.JoinHostPort(host, port))
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if _, err := fmt.Fprintf(f, "%s %s\n", marker, line); err != nil {
		return err
	}
	return nil
}

// sha256FingerprintHex is a small helper reused by the TLS pin path.
func sha256FingerprintHex(der []byte) string {
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:])
}
