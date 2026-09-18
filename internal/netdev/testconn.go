package netdev

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// TestConnection runs the first-device flow: connect → TOFU (captured, not
// interactive) → CLI session (paging-off exercises the prompt state machine).
// The result tells the UI exactly what to do next: trust the host key, fix
// credentials, or celebrate.
//
// Two-step TOFU: the strict prompt captures the presented key and rejects, so
// no code path can silently trust anything — the human confirms the
// fingerprint in the UI and calls TrustHostKey with the SAME fingerprint.
type TestResult struct {
	Device      string `json:"device"`
	Status      string `json:"status"` // ok | unknown-host-key | auth-failed | refused-by-classifier | error
	Detail      string `json:"detail,omitempty"`
	Host        string `json:"host,omitempty"`
	KeyType     string `json:"keyType,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Test statuses.
const (
	TestOK             = "ok"
	TestUnknownHostKey = "unknown-host-key"
	TestAuthFailed     = "auth-failed"
	TestError          = "error"
)

// trustCache holds first-seen keys captured during a test dial, keyed by
// fingerprint so TrustHostKey can append the exact key the human confirmed.
var (
	trustMu    sync.Mutex
	trustCache = map[string]capturedKey{}
)

type capturedKey struct {
	hostname string
	addr     net.Addr
	key      ssh.PublicKey
}

// TestConnection dials one configured device and opens (then closes) a CLI
// session. It never prompts interactively.
func (m *Manager) TestConnection(ctx context.Context, deviceName string) TestResult {
	device, ok := m.cfg.NetDevDeviceByName(deviceName)
	if !ok {
		return TestResult{Device: deviceName, Status: TestError, Detail: "device not in inventory"}
	}
	if _, ok := m.driverFor(device); !ok {
		return TestResult{Device: deviceName, Status: TestError,
			Detail: fmt.Sprintf("no driver for %s/%s", device.Vendor, device.OS)}
	}

	var captured *transport.HostKeyQuestion
	policy := &transport.HostKeyPolicy{
		// Capture records the raw key the moment TOFU fires (see connectWith);
		// Prompt then rejects — nothing is trusted without the human.
		Prompt: func(ctx context.Context, q transport.HostKeyQuestion) (bool, error) {
			captured = &q
			return false, nil
		},
	}

	client, session, err := m.connectWith(ctx, device, policy)
	if err != nil {
		if captured != nil {
			return TestResult{
				Device: deviceName, Status: TestUnknownHostKey,
				Detail: "first-seen host key — confirm the fingerprint to trust it",
				Host:   captured.Host, KeyType: captured.KeyType, Fingerprint: captured.Fingerprint,
			}
		}
		if errors.Is(err, transport.ErrAuthFailed) {
			return TestResult{Device: deviceName, Status: TestAuthFailed, Detail: err.Error()}
		}
		if errors.Is(err, transport.ErrHostKeyMismatch) {
			return TestResult{Device: deviceName, Status: TestError,
				Detail: "host key MISMATCH vs known_hosts — possible MITM, inspect the record manually (never promptable)"}
		}
		return TestResult{Device: deviceName, Status: TestError, Detail: err.Error()}
	}
	defer client.Close()
	defer session.Close()
	// OpenSession already ran the driver's paging-off commands: reaching here
	// proves the prompt state machine works on this device.
	return TestResult{Device: deviceName, Status: TestOK, Detail: "connected; CLI session verified (paging-off accepted)"}
}

// connectWith is connect() with an injectable host-key policy (tests + the
// TOFU capture flow). The key capture rides on the prompt callback, so the
// policy wrapper records what was presented before rejecting.
func (m *Manager) connectWith(ctx context.Context, d config.NetDevDevice, policy *transport.HostKeyPolicy) (*transport.Client, *Session, error) {
	drv, _ := m.driverFor(d)
	lookup := m.lookupEntry()
	resolved, err := transport.ResolveHost(lookup, d.Name, nil)
	if err != nil {
		return nil, nil, err
	}
	jumps, err := transport.ResolveJumpHosts(lookup, d.Via, nil)
	if err != nil {
		return nil, nil, err
	}
	// Build the dial policy from the caller's pieces (copying HostKeyPolicy
	// by value would copy its mutex — construct instead).
	wrapped := &transport.HostKeyPolicy{
		Prompt: policy.Prompt,
		Capture: func(h string, remote net.Addr, key ssh.PublicKey) {
			recordCapturedKey(h, remote, key)
		},
	}
	auth := transport.AuthOptions{
		Password:   secretReader(SecretKindPassword, d.PasswordEnv),
		Passphrase: secretReader(SecretKindPassphrase, d.PassphraseEnv),
	}
	hops := make([]transport.JumpHostOptions, 0, len(jumps))
	for i, j := range jumps {
		hopCfg := m.hopByRaw(d.Via[i])
		hops = append(hops, transport.JumpHostOptions{Host: j, Auth: transport.AuthOptions{
			Password:   secretReader(SecretKindPassword, hopCfg.PasswordEnv),
			Passphrase: secretReader(SecretKindPassphrase, hopCfg.PassphraseEnv),
		}})
	}
	client, err := transport.New(transport.Options{
		Host: resolved, Auth: auth, JumpHosts: hops, HostKeys: wrapped,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := client.Start(ctx); err != nil {
		client.Close()
		return nil, nil, err
	}
	session, err := OpenSession(ctx, client, drv, d.Encoding)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return client, session, nil
}

// fakeAddr carries the captured remote address string through the trust flow
// (knownhosts.Normalize accepts the string form).
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func capturedKeyStruct(hostname string, addr net.Addr, key ssh.PublicKey) capturedKey {
	return capturedKey{hostname: hostname, addr: addr, key: key}
}

// TrustHostKey durably trusts a first-seen key the human confirmed. The
// fingerprint must match one captured by a recent TestConnection — trusting an
// arbitrary fingerprint without the presented key would record nothing real.
func TrustHostKey(fingerprint string) error {
	trustMu.Lock()
	ck, ok := trustCache[strings.TrimSpace(fingerprint)]
	if ok {
		delete(trustCache, strings.TrimSpace(fingerprint)) // one-shot
	}
	trustMu.Unlock()
	if !ok {
		return fmt.Errorf("no captured host key matches %q — run the connection test first", fingerprint)
	}
	if ck.key == nil {
		// The public key material itself is not retained through the question
		// path; re-derive it from the address by asking the user to retest.
		// (Not reachable in the current flow — kept as a guard.)
		return fmt.Errorf("captured entry lacks key material — retest the connection")
	}
	return transport.TrustKey(ck.hostname, ck.addr, ck.key)
}

// recordCapturedKey stores a presented key for the two-step flow. Exported for
// the transport-layer hook when the prompt fires with raw key material.
func recordCapturedKey(hostname string, addr net.Addr, key ssh.PublicKey) {
	trustMu.Lock()
	trustCache[ssh.FingerprintSHA256(key)] = capturedKey{hostname: hostname, addr: addr, key: key}
	trustMu.Unlock()
}
