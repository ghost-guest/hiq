package netdev

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// auditChainHash computes one entry's chain hash: sha256(prevHash + canonical
// JSON of the entry with its hash field empty).
func auditChainHash(prev string, e Audit) (string, error) {
	e.Hash = ""
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(prev), b...))
	return hex.EncodeToString(sum[:]), nil
}

// Audit records every device interaction in an append-only JSONL file under
// <user config>/hiq/netdev/audit.jsonl — one of the never-off guardrails
// (NETDEV_SPEC invariant 4). Command text and outcome are recorded; raw
// output is deliberately NOT stored (device output can carry secrets — the
// redactor and evidence integration land in P2; until then only the size and
// a hash are kept).
type Audit struct {
	Time        time.Time `json:"time"`
	Device      string    `json:"device"`
	Via         []string  `json:"via,omitempty"`
	Command     string    `json:"command"`
	Class       string    `json:"class"`  // read | write | dangerous | unknown (classifier verdict)
	Status      string    `json:"status"` // ok | device-error | refused | failure
	OutputBytes int       `json:"output_bytes,omitempty"`
	Error       string    `json:"error,omitempty"`
	// Hash chains entries (B-batch): sha256 over the previous entry's hash and
	// this entry's canonical JSON (hash field excluded). Any tampering with an
	// old line — or a line removed — breaks every later hash. Entries written
	// before the chain landed have empty hashes; the chain starts at the first
	// hashed entry.
	Hash string `json:"hash,omitempty"`
}

// Audit statuses.
const (
	AuditOK          = "ok"
	AuditDeviceError = "device-error"
	AuditRefused     = "refused"
	AuditFailure     = "failure"
)

var (
	auditMu   sync.Mutex
	auditPath string // overridden in tests
)

// SetAuditPath overrides the audit file location (tests). Switching files
// invalidates the cached chain head — chaining a fresh file off the previous
// file's head would break verification at its very first line.
func SetAuditPath(p string) {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditPath = p
	auditLastHash = ""
}

func auditFile() string {
	auditMu.Lock()
	defer auditMu.Unlock()
	if auditPath != "" {
		return auditPath
	}
	return filepath.Join(netdevStateDir(), "audit.jsonl")
}

// AuditPath returns the audit file location (test override included).
func AuditPath() string {
	if p := auditPathLocked(); p != "" {
		return p
	}
	return filepath.Join(netdevStateDir(), "audit.jsonl")
}

func auditPathLocked() string {
	auditMu.Lock()
	defer auditMu.Unlock()
	return auditPath
}

// netdevStateDir is the netdev state directory beside secrets.enc.json.
var netdevStateDirOverr string

func netdevStateDir() string {
	if netdevStateDirOverr != "" {
		return netdevStateDirOverr
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}
	return filepath.Join(dir, "hiq", "netdev")
}

// AppendAudit writes one entry. Failures are returned (the caller logs); an
// audit failure must not silently vanish — but it also must not block the
// diagnostic hand (the command already ran; the audit gap is itself
// observable in ops review).
func AppendAudit(e Audit) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	// Chain: hash = sha256(prevHash + canonical(entry without hash)). The whole
	// read-prev → hash → append → cache-update sequence is one critical section:
	// computing the hash before taking the lock let two concurrent appends chain
	// off the same prev, silently breaking VerifyAuditChain.
	prev, err := lastAuditHashLocked()
	if err != nil {
		return err
	}
	h, err := auditChainHash(prev, e)
	if err != nil {
		return err
	}
	e.Hash = h
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := auditPath
	if path == "" {
		path = filepath.Join(netdevStateDir(), "audit.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	auditLastHash = h
	// Pass the just-written head: reading it back via AuditChainHead would
	// re-lock auditMu while we still hold it (non-reentrant deadlock).
	maybeAnchorAudit(h)
	return nil
}

// AuditChainHead returns the local audit chain head ("" when nothing is
// chained yet) — the value cross-anchored to the trust domain (spec §八).
func AuditChainHead() string {
	h, err := lastAuditHash()
	if err != nil {
		return ""
	}
	return h
}

// auditLastHash caches the chain head (the last written entry's hash).
var auditLastHash string

// lastAuditHash returns the chain head, reading the file's last line on a
// cold cache.
func lastAuditHash() (string, error) {
	auditMu.Lock()
	defer auditMu.Unlock()
	return lastAuditHashLocked()
}

// lastAuditHashLocked is lastAuditHash with the caller holding auditMu.
func lastAuditHashLocked() (string, error) {
	if auditLastHash != "" {
		return auditLastHash, nil
	}
	// The cold-cache read must NOT go through AuditPath()/readAuditLines() —
	// both re-lock auditMu (non-reentrant) and would deadlock under AppendAudit.
	lines, err := readAuditLinesAt(currentAuditPathLocked())
	if err != nil {
		return "", err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var e Audit
		if json.Unmarshal(lines[i], &e) == nil && e.Hash != "" {
			return e.Hash, nil
		}
	}
	return "", nil
}

// currentAuditPathLocked resolves the audit file path from the guarded field
// directly; caller holds auditMu (or accepts racing a concurrent SetAuditPath,
// which only tests perform).
func currentAuditPathLocked() string {
	if auditPath != "" {
		return auditPath
	}
	return filepath.Join(netdevStateDir(), "audit.jsonl")
}

func readAuditLines() ([][]byte, error) {
	return readAuditLinesAt(AuditPath())
}

func readAuditLinesAt(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out, nil
}

// AuditChainStatus is the verification verdict for the audit tab's badge.
type AuditChainStatus struct {
	Total       int    `json:"total"`
	Chained     int    `json:"chained"`
	OK          bool   `json:"ok"`
	FirstBroken string `json:"firstBroken,omitempty"`
}

// VerifyAuditChain re-computes every hashed entry's hash in sequence. Legacy
// entries without hashes are skipped (the chain starts at the first hashed
// entry); a mismatch marks the chain broken from that entry on.
func VerifyAuditChain() AuditChainStatus {
	lines, err := readAuditLines()
	if err != nil {
		return AuditChainStatus{OK: false, FirstBroken: "读取审计文件失败: " + err.Error()}
	}
	st := AuditChainStatus{Total: len(lines), OK: true}
	prev := ""
	for i, l := range lines {
		var e Audit
		if json.Unmarshal(l, &e) != nil {
			st.OK = false
			st.FirstBroken = fmt.Sprintf("第 %d 行无法解析", i+1)
			return st
		}
		if e.Hash == "" {
			continue // legacy pre-chain entry
		}
		want, err := auditChainHash(prev, e)
		if err != nil {
			st.OK = false
			st.FirstBroken = fmt.Sprintf("第 %d 行哈希计算失败", i+1)
			return st
		}
		if want != e.Hash {
			st.OK = false
			st.FirstBroken = fmt.Sprintf("第 %d 行哈希不匹配（审计可能被篡改）", i+1)
			return st
		}
		prev = e.Hash
		st.Chained++
	}
	return st
}
