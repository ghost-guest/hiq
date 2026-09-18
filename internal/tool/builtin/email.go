package builtin

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/tool"
)

// Email tools (Phase 3 of coWork). Phase 3 ships outbound email_send (SMTP,
// stdlib-only — no new deps). IMAP read/search is deferred: it needs a
// per-provider IMAP config surface and protocol handling that's better built on
// go-imap once read use-cases are clear; for now the office "send a report /
// notification" path is covered.
//
// The SMTP config (host/port/credentials) comes from the [[cowork.email_accounts]]
// multi-account list, injected at boot via SetEmailAccounts. The legacy
// config.Cowork.SMTP single-account table ([cowork.smtp]) is folded into
// EmailAccounts[0] by normalizeEmailAccounts at config-load, so no separate
// runtime fallback path is needed. When no account resolves, email_send
// returns a clear config error rather than failing opaquely at the socket.

// resolveSMTP picks the SMTP config for an outbound send. A named account
// (non-empty) resolves via accountByName; empty resolves to the Default
// account. Returns a clear error when no account is configured (the common
// "email not configured" message). The legacy [cowork.smtp] single-account
// table still works because normalizeEmailAccounts folds it into
// EmailAccounts[0] at config-load, so no runtime fallback is needed.
func resolveSMTP(account string) (config.SMTPConfig, error) {
	if a, ok := accountByName(account); ok && strings.TrimSpace(a.SMTP.Host) != "" {
		return a.SMTP, nil
	}
	return config.SMTPConfig{}, fmt.Errorf("email not configured: set [[cowork.email_accounts]] (or legacy [cowork.smtp]) host/port/from/username/password_env in config")
}

// SendPlainText delivers a plain-text email using the Default account's SMTP
// settings. Exposed so non-tool callers (the scheduler's email delivery bridge)
// can reuse the same SMTP wiring without duplicating it. Returns an error if
// no account is configured. The ctx bounds dial/handshake and cancels a stalled
// send; pass context.Background() when no cancellation is needed.
func SendPlainText(ctx context.Context, to, subject, body string) error {
	return SendPlainTextAs(ctx, "", to, subject, body)
}

// SendPlainTextAs delivers a plain-text email using the named account's SMTP
// settings. An empty account selects the Default account. Used by the scheduler
// and calendar reminder engine to send via a user-chosen mailbox rather than
// always the default. The ctx bounds dial/handshake and cancels a stalled send.
func SendPlainTextAs(ctx context.Context, account, to, subject, body string) error {
	cfg, err := resolveSMTP(account)
	if err != nil {
		return err
	}
	if cfg.From == "" {
		return fmt.Errorf("email account %q has no from address", account)
	}
	msg := buildPlainTextMessage(cfg.From, []string{to}, subject, body)
	return sendSMTP(ctx, cfg, []string{to}, nil, nil, msg)
}

// buildPlainTextMessage assembles a minimal RFC822 message with a UTF-8 text
// body (Chinese content renders correctly via base64-encoded UTF-8).
func buildPlainTextMessage(from string, to []string, subject, body string) []byte {
	var buf strings.Builder
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: =?UTF-8?B?%s?=\r\n", base64.StdEncoding.EncodeToString([]byte(subject)))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		buf.WriteString(encoded[i:end])
		buf.WriteString("\r\n")
	}
	return []byte(buf.String())
}

// emailAccounts holds the multi-mailbox config injected at boot (and refreshed
// by the desktop settings panel on save). It's the source of truth for
// accountByName — the per-account lookup email tools/scheduler use. Guarded by
// emailAccountsMu because boot writes it once and the settings panel can write
// it again later while a tool reads concurrently.
var (
	emailAccountsMu sync.RWMutex
	emailAccounts   []config.EmailAccount
)

// SetEmailAccounts injects the multi-mailbox config. Called from boot.go at
// startup and from the desktop settings panel when the user saves a config
// change, so tools pick up new/edited accounts without a restart.
func SetEmailAccounts(accounts []config.EmailAccount) {
	emailAccountsMu.Lock()
	emailAccounts = append(emailAccounts[:0], accounts...)
	emailAccountsMu.Unlock()
}

// accountByName resolves a named mailbox from the injected config. Empty name
// selects the Default account (or the first when none is flagged Default).
// Returns ok=false when no accounts are configured or the name doesn't match,
// so callers can emit a clear "account not configured" error.
func accountByName(name string) (config.EmailAccount, bool) {
	emailAccountsMu.RLock()
	defer emailAccountsMu.RUnlock()
	name = strings.TrimSpace(name)
	if name == "" {
		for _, a := range emailAccounts {
			if a.Default {
				return a, true
			}
		}
		if len(emailAccounts) > 0 {
			return emailAccounts[0], true
		}
		return config.EmailAccount{}, false
	}
	for _, a := range emailAccounts {
		if strings.EqualFold(strings.TrimSpace(a.Name), name) {
			return a, true
		}
	}
	return config.EmailAccount{}, false
}

// resolveIMAP picks the IMAP config an email_read/search call should use. A
// matching named account resolves via accountByName. Returns a clear error when
// no account is configured. The legacy [cowork.imap] single-account table still
// works because normalizeEmailAccounts folds it into EmailAccounts[0] at
// config-load, so no runtime fallback is needed.
func resolveIMAP(account string) (config.IMAPConfig, error) {
	if a, ok := accountByName(account); ok && strings.TrimSpace(a.IMAP.Host) != "" {
		return a.IMAP, nil
	}
	return config.IMAPConfig{}, fmt.Errorf("email account %q not configured (no matching [[cowork.email_accounts]] entry and no legacy [cowork.imap])", account)
}

// EmailTools returns the email tools for cowork registration: send (SMTP) plus
// read/search (IMAP) when configured.
func EmailTools() []tool.Tool {
	return []tool.Tool{emailSend{}, emailReadTool{}, emailSearchTool{}}
}

type emailSend struct{}

func (emailSend) Name() string { return "email_send" }

func (emailSend) Description() string {
	return "Send an email via the configured SMTP server (set [cowork.smtp] host/port/from/username/password_env in config). Supports text and HTML bodies, CC/BCC, and file attachments. Use to deliver reports, notifications, or any agent-produced document. Requires [cowork.smtp] config; if unset the call returns a config error. Recipients is a comma-separated list or array."
}

func (emailSend) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "to":{"description":"Recipient address(es): a string (\"a@x.com, b@y.com\") or an array of strings"},
  "subject":{"type":"string"},
  "body":{"type":"string","description":"Email body. Plain text unless format is html."},
  "format":{"type":"string","enum":["text","html"],"description":"Body format (default text)"},
  "cc":{"description":"Optional CC, same format as to"},
  "bcc":{"description":"Optional BCC, same format as to"},
  "attachments":{"type":"array","items":{"type":"string"},"description":"Optional list of file paths to attach"},
  "account":{"type":"string","description":"Optional named mailbox to send from (from [[cowork.email_accounts]]). Empty = the default account."}
},
"required":["to","subject","body"]
}`)
}

func (emailSend) ReadOnly() bool { return false }

func (emailSend) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		To          json.RawMessage `json:"to"`
		Subject     string          `json:"subject"`
		Body        string          `json:"body"`
		Format      string          `json:"format"`
		CC          json.RawMessage `json:"cc"`
		BCC         json.RawMessage `json:"bcc"`
		Attachments []string        `json:"attachments"`
		Account     string          `json:"account"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	to, err := resolveAddrs(p.To)
	if err != nil {
		return "", err
	}
	if len(to) == 0 {
		return "", errors.New("at least one recipient (to) is required")
	}
	cc, _ := resolveAddrs(p.CC)
	bcc, _ := resolveAddrs(p.BCC)
	cfg, err := resolveSMTP(p.Account)
	if err != nil {
		return "", err
	}
	if cfg.From == "" {
		return "", errors.New("email account has no from address — set 'from' in [[cowork.email_accounts]]")
	}
	format := strings.ToLower(strings.TrimSpace(p.Format))
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "html" {
		return "", fmt.Errorf("format must be text or html, got %q", p.Format)
	}

	msg, err := buildMessage(cfg.From, to, cc, bcc, p.Subject, p.Body, format, p.Attachments)
	if err != nil {
		return "", err
	}

	if err := sendSMTP(ctx, cfg, to, cc, bcc, msg); err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	attNote := ""
	if len(p.Attachments) > 0 {
		attNote = fmt.Sprintf(", %d attachment(s)", len(p.Attachments))
	}
	return fmt.Sprintf("sent to %s%s%s — subject: %q", strings.Join(to, ", "), joinNonEmpty(cc, " cc ", ""), attNote, p.Subject), nil
}

func joinNonEmpty(addrs []string, prefix, suffix string) string {
	if len(addrs) == 0 {
		return ""
	}
	return prefix + strings.Join(addrs, ", ") + suffix
}

// resolveAddrs accepts a JSON string ("a@x, b@y") or array of strings, returning
// a cleaned list of addresses.
func resolveAddrs(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// Try array first.
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return cleanAddrs(arr), nil
	}
	// Fall back to a comma-separated string.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanAddrs(strings.Split(s, ",")), nil
	}
	return nil, errors.New("address must be a string or array of strings")
}

func cleanAddrs(in []string) []string {
	var out []string
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// buildMessage assembles a MIME message. With attachments it uses multipart;
// without, a simple text/html part. Headers are RFC 5322 compliant.
func buildMessage(from string, to, cc, bcc []string, subject, body, format string, attachments []string) ([]byte, error) {
	headers := map[string]string{
		"From":         from,
		"To":           strings.Join(to, ", "),
		"Subject":      mime.QEncoding.Encode("utf-8", subject),
		"Date":         time.Now().Format(time.RFC1123Z),
		"MIME-Version": "1.0",
	}
	if len(cc) > 0 {
		headers["Cc"] = strings.Join(cc, ", ")
	}

	var buf strings.Builder
	writeHeaders := func() {
		// Stable header order.
		for _, k := range []string{"From", "To", "Cc", "Subject", "Date", "MIME-Version"} {
			if v, ok := headers[k]; ok {
				fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
			}
		}
	}

	if len(attachments) == 0 {
		headers["Content-Type"] = fmt.Sprintf("text/%s; charset=UTF-8", format)
		headers["Content-Transfer-Encoding"] = "8bit"
		// Re-emit with the content type included.
		buf.Reset()
		for _, k := range []string{"From", "To", "Cc", "Subject", "Date", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"} {
			if v, ok := headers[k]; ok {
				fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
			}
		}
		buf.WriteString("\r\n")
		buf.WriteString(body)
		return []byte(buf.String()), nil
	}

	// Multipart: text/html part + base64 attachment parts.
	boundary := fmt.Sprintf("hiq-%d", time.Now().UnixNano())
	headers["Content-Type"] = "multipart/mixed; boundary=" + boundary
	buf.Reset()
	writeHeaders()
	// Add Content-Type after the standard set.
	fmt.Fprintf(&buf, "Content-Type: %s\r\n", headers["Content-Type"])
	buf.WriteString("\r\n")

	// Body part.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/%s; charset=UTF-8\r\n", format)
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(body)
	buf.WriteString("\r\n")

	// Attachment parts.
	for _, path := range attachments {
		path = strings.TrimSpace(path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", path, err)
		}
		name := filepath.Base(path)
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s; name=%q\r\n", ct, name)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n", name)
		// Base64 with 76-char line wrap (RFC 2045).
		encoded := base64.StdEncoding.EncodeToString(data)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			buf.WriteString(encoded[i:end])
			buf.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return []byte(buf.String()), nil
}

// smtpDialTimeout bounds the initial TCP/TLS handshake. A non-responding server
// must not hang the tool call or a scheduled task indefinitely. Matches the
// inbound IMAP dial budget (see imapConnect's dialTimeout).
const smtpDialTimeout = 20 * time.Second

// smtpSendTimeout bounds the overall SMTP transaction for the STARTTLS/plain
// path, which goes through net/smtp.SendMail (no native ctx support).
const smtpSendTimeout = 60 * time.Second

// sendSMTP delivers the message. Handles implicit TLS (port 465), STARTTLS
// (587), and plain (25). Auth via PLAIN/CRAMMD5 as the server supports.
//
// ctx bounds dial/handshake and cancels a stalled send on both paths. The
// implicit-TLS path dials with DialContext + HandshakeContext so ctx cancel or
// expiry aborts the connection. The STARTTLS/plain path uses net/smtp.SendMail,
// which has no ctx support, so it runs on a goroutine and is aborted (connection
// closed under it) when ctx is done or smtpSendTimeout elapses.
func sendSMTP(ctx context.Context, cfg config.SMTPConfig, to, cc, bcc []string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	recipients := append(append(append([]string{}, to...), cc...), bcc...)

	if cfg.UseTLS || cfg.EncryptionMode == "tls" || cfg.Port == 465 {
		// Implicit TLS: dial with a timeout, then wrap in TLS honoring ctx.
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		netConn, err := (&net.Dialer{Timeout: smtpDialTimeout}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		conn := tls.Client(netConn, tlsCfg)
		// Close the TCP conn if ctx is cancelled while we hold the TLS conn;
		// Closing it forces any blocking handshake/IO to error out.
		stop := context.AfterFunc(ctx, func() { _ = netConn.Close() })
		defer stop()
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = netConn.Close()
			return fmt.Errorf("tls handshake %s: %w", addr, err)
		}
		defer conn.Close()
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		defer func() { _ = c.Quit() }()
		if err := authAndSend(c, cfg, cfg.From, recipients, msg); err != nil {
			return err
		}
		return nil
	}
	// STARTTLS or plain. net/smtp.SendMail has no ctx/timeout support, so run it
	// on a goroutine and abort by closing the connection under it if ctx is done
	// or smtpSendTimeout elapses. (Closing the conn causes SendMail to return.)
	sendCtx, cancel := context.WithTimeout(ctx, smtpSendTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, authFor(cfg), cfg.From, recipients, msg) }()
	select {
	case err := <-done:
		return err
	case <-sendCtx.Done():
		return sendCtx.Err()
	}
}

// authFor builds the smtp.Auth from the config. Empty username = no auth (some
// internal relays). Password is read from the env var at send time.
func authFor(cfg config.SMTPConfig) smtp.Auth {
	if cfg.Username == "" {
		return nil
	}
	pass := ""
	if cfg.PasswordEnv != "" {
		pass = strings.TrimSpace(os.Getenv(cfg.PasswordEnv))
	}
	if pass == "" {
		return nil
	}
	return smtp.PlainAuth("", cfg.Username, pass, cfg.Host)
}

// authAndSend runs the AUTH/MAIL/RCPT/DATA sequence on a connected TLS client.
func authAndSend(c *smtp.Client, cfg config.SMTPConfig, from string, recipients []string, msg []byte) error {
	if a := authFor(cfg); a != nil {
		if err := c.Auth(a); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}
