package builtin

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/zzycxz/hiq/internal/config"
)

// IMAP email read/search — powered by go-imap + go-message (production IMAP
// client + MIME parser), replacing the earlier hand-rolled stdlib client. This
// gives protocol-level correctness: full SEARCH criteria, proper literal/FETCH
// handling, RFC 2047 encoded-word decoding, multipart MIME bodies, and character
// set conversion — the hand-rolled version approximated these and would fail on
// real-world mail (encoded subjects, HTML alternatives, attachments).
//
// The tool surface (email_read, email_search) and config ([cowork.imap]) are
// unchanged — this is a drop-in implementation upgrade. When IMAP isn't
// configured, the tools return a clear error.

// EmailAttachment is metadata for one email attachment.
type EmailAttachment struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// EmailMessage is the read/search result: envelope fields + a body preview.
type EmailMessage struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	Subject     string            `json:"subject"`
	Date        string            `json:"date"` // RFC3339 ("2006-01-02T15:04:05Z07:00"), ISO 8601 so the JS frontend parses it reliably
	Preview     string            `json:"preview"`
	Attachments []EmailAttachment `json:"attachments,omitempty"`
	// rawDate is the parsed envelope date, kept (unexported) so callers can sort
	// newest-first deterministically instead of relying on IMAP arrival order.
	rawDate time.Time
}

// imapConnect dials (TLS for 993, plain/STARTTLS for 143), logs in. Returns the
// client (caller closes) or an error.
func imapConnect(ctx context.Context, cfg config.IMAPConfig) (*client.Client, error) {
	port := cfg.Port
	if port == 0 {
		port = 993
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	// Bound the TCP/TLS handshake so a hung or unreachable server fails fast
	// instead of blocking forever. go-imap v1's DialTLS/Dial do not accept a
	// context, so we pass a net.Dialer with a Timeout (the library honors it as
	// the dial deadline). A subsequent per-command deadline is set via Timeout.
	const dialTimeout = 20 * time.Second
	dialer := &net.Dialer{Timeout: dialTimeout}

	var c *client.Client
	var err error
	if port == 993 {
		// Try strict TLS first; on handshake failure, retry with RSA key-exchange
		// ciphers re-enabled. Go 1.22+ removed RSA key-exchange suites from the
		// default list, but some providers (notably 139.com) only support RSA key
		// exchange — without these the handshake always fails. Certificate
		// verification stays ON unless the user opted in via skip_tls_verify.
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		c, err = client.DialWithDialerTLS(dialer, addr, tlsCfg)
		if err != nil && isTLSError(err) {
			tlsCfg.InsecureSkipVerify = cfg.SkipTLSVerify
			tlsCfg.CipherSuites = []uint16{
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			}
			c, err = client.DialWithDialerTLS(dialer, addr, tlsCfg)
		}
	} else {
		c, err = client.DialWithDialer(dialer, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	// Per-command deadline so a stalled SELECT/SEARCH/Fetch can't hang the tool.
	c.Timeout = 30 * time.Second
	pass := imapPassword(cfg)
	if err := c.Login(cfg.Username, pass); err != nil {
		_ = c.Logout()
		return nil, classifyAuthErr(fmt.Errorf("imap login (check credentials): %w", err))
	}
	return c, nil
}

// ProbeAccountIMAP cheaply verifies that account (default when empty) can
// connect + log in to IMAP, without fetching mail. Returns nil when the account
// has no IMAP host (a send-only setup) so it doesn't block send-only tasks. On
// an auth failure it returns a friendly error and fires the auth-expired toast.
// Used to surface an expired 139 authorization code (90-day life) up front,
// before a scheduled task burns tokens.
func ProbeAccountIMAP(account string) error {
	a, ok := accountByName(account)
	if !ok {
		return errors.New("email account not configured")
	}
	if strings.TrimSpace(a.IMAP.Host) == "" {
		return nil // send-only account — nothing to probe over IMAP
	}
	return ProbeIMAPConfig(a.IMAP)
}

// ReadInboxFor reads the most recent `limit` messages from a mailbox (INBOX by
// default; "Sent" for the sent view). Unread-only when unreadOnly=true (ignored
// for non-INBOX). Exported so the cowork dock's "邮件" tab can preview mail
// WITHOUT going through the agent tool path. Same pattern as ProbeIMAPConfig:
// standalone config, own timeout, friendly error on auth/connection failure.
func ReadInboxFor(cfg config.IMAPConfig, mailbox string, limit int, unreadOnly bool) ([]EmailMessage, error) {
	if limit <= 0 {
		limit = 30
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return imapRead(ctx, cfg, mailbox, limit, unreadOnly, false, time.Time{}, time.Time{})
}

// ProbeIMAPConfig verifies a standalone IMAP config can connect + log in +
// select INBOX, without fetching mail. Exported so the desktop settings panel
// can probe a freshly saved mailbox WITHOUT going through the global
// emailAccounts slice (which is only refreshed on boot, not on every save).
// Returns a friendly error on auth failure. Caller sets the timeout.
func ProbeIMAPConfig(cfg config.IMAPConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := imapConnect(ctx, cfg)
	if err != nil {
		return friendlyEmailErr("", err)
	}
	if _, err := c.Select("INBOX", true); err != nil {
		_ = c.Logout()
		return fmt.Errorf("select inbox: %w", err)
	}
	return c.Logout()
}

// isTLSError reports whether err looks like a TLS handshake failure.
func isTLSError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "handshake") ||
		strings.Contains(s, "tls:") ||
		strings.Contains(s, "SSL") ||
		strings.Contains(s, "certificate")
}

// decodeRFC2047 decodes RFC 2047 encoded words (e.g. =?gbk?b?...?=) in header
// values. The standard mime.WordDecoder handles UTF-8/Latin-1 but does NOT know
// GBK/GB2312/GB18030 — the charsets used by 139.com and many Chinese
// mailers use. We first try the standard decoder (for UTF-8 etc.), then fall
// back to a manual pass that handles the GBK family via golang.org/x/text, so
// Chinese subjects/senders render correctly instead of showing the raw
// "=?gb2312?B?...?=" blob.
func decodeRFC2047(s string) string {
	// Fast path: standard library handles utf-8/iso-8859-* and already-decoded
	// text. If it returns something with no remaining encoded-words, we're done.
	dec := &mime.WordDecoder{}
	if d, err := dec.DecodeHeader(s); err == nil && !encodedWordRe.MatchString(d) {
		return d
	}
	// Slow path: manually decode encoded-words, supporting GBK/GB2312/GB18030/
	// Big5 that the stdlib's mime package omits.
	return decodeRFC2047Manual(s)
}

// encodedWordRe matches a single RFC 2047 encoded-word token =?charset?enc?text?
var encodedWordRe = regexp.MustCompile(`=\?([^?]+)\?([BbQq])\?([^?]*)\?=`)

// decodeRFC2047Manual walks the string, decoding each encoded-word with a
// charset resolver that covers the GBK family. Non-encoded runs are copied
// verbatim. Unknown charsets fall back to the stdlib decoder (utf-8/latin1).
func decodeRFC2047Manual(s string) string {
	var b strings.Builder
	lastEnd := 0
	for _, m := range encodedWordRe.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[0], m[1]
		charset := s[m[2]:m[3]]
		enc := strings.ToUpper(s[m[4]:m[5]])
		encoded := s[m[6]:m[7]]
		// Copy the literal run before this token.
		b.WriteString(s[lastEnd:start])
		if decoded, ok := decodeEncodedWord(charset, enc, encoded); ok {
			b.WriteString(decoded)
		} else {
			b.WriteString(s[start:end]) // leave untouched on failure
		}
		lastEnd = end
	}
	b.WriteString(s[lastEnd:])
	return b.String()
}

// decodeEncodedWord decodes one =?charset?enc?text? token. Returns ok=false
// when the charset/encoding is unrecognized so the caller keeps the raw token.
func decodeEncodedWord(charset, enc, encoded string) (string, bool) {
	// Step 1: undo the transfer encoding (B = base64, Q = quoted-printable).
	var raw []byte
	switch enc {
	case "B":
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
		raw = b
	case "Q":
		// RFC 2047 Q-encoding: '_' means space, '=' is the QP escape.
		q := strings.ReplaceAll(encoded, "_", " ")
		b, err := qprintableDecode(q)
		if err != nil {
			return "", false
		}
		raw = b
	default:
		return "", false
	}
	// Step 2: decode the charset to UTF-8.
	dec := charsetToDecoder(charset)
	if dec == nil {
		return "", false // unknown charset — leave raw
	}
	utf8, _, err := transform.String(dec, string(raw))
	if err != nil {
		return "", false
	}
	return utf8, true
}

// qprintableDecode decodes a (loose) quoted-printable string. The stdlib
// mime/quotedprintable reader is stricter than RFC 2047's Q-variant needs.
func qprintableDecode(s string) ([]byte, error) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '=' {
			if i+2 >= len(s) {
				return nil, fmt.Errorf("truncated QP escape")
			}
			hexVal, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return nil, err
			}
			out.WriteByte(byte(hexVal))
			i += 2
		} else {
			out.WriteByte(c)
		}
	}
	return []byte(out.String()), nil
}

// charsetToDecoder returns an x/text decoder (a transform.Transformer) for the
// GBK family + Big5, which the standard mime package omits. GB18030 is a
// superset of GBK/GB2312, so it decodes all three. Returns nil for charsets the
// stdlib already handles (utf-8, iso-8859-*), so the caller can fall back.
func charsetToDecoder(charset string) transform.Transformer {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "gbk", "gb18030", "csgb2312":
		// GB18030 is a superset of GBK and GB2312.
		return simplifiedchinese.GB18030.NewDecoder()
	case "gb2312":
		// Pure GB2312 maps best to GBK (a strict superset of GB2312); GB18030
		// also works but GBK is the conventional choice for legacy GB2312 mail.
		return simplifiedchinese.GBK.NewDecoder()
	}
	return nil
}

func imapPassword(cfg config.IMAPConfig) string {
	if cfg.PasswordEnv != "" {
		return strings.TrimSpace(osGetenv(cfg.PasswordEnv))
	}
	return ""
}

// imapRead selects a mailbox (INBOX by default), fetches the most recent `limit`
// messages (or unread only). Returns envelopes + a plain-text body preview per
// message if withBody is true. mailbox may be "INBOX" or a sent folder name;
// when empty it defaults to INBOX. For sent folders the unread flag filter is
// ignored (sent mail has no "unread" concept the user cares about).
func imapRead(ctx context.Context, cfg config.IMAPConfig, mailbox string, limit int, unreadOnly bool, withBody bool, since, before time.Time) ([]EmailMessage, error) {
	c, err := imapConnect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout() }()

	mbox := strings.TrimSpace(mailbox)
	if mbox == "" {
		mbox = "INBOX"
	}
	if _, err := c.Select(mbox, true); err != nil { // read-only
		// The requested mailbox may not exist under that exact name — providers
		// use "Sent", "Sent Messages", "已发送", "[Gmail]/Sent Mail", etc. Try
		// all common aliases and pick the one with the MOST messages (some
		// providers have a near-empty "Sent" alongside the real "已发送" with
		// hundreds of messages — picking the first alias that exists would miss
		// the real sent folder).
		if mbox != "INBOX" {
			bestMbox := ""
			bestCount := -1
			for _, alias := range []string{"已发送", "已发送邮件", "Sent Messages", "Sent", "[Gmail]/Sent Mail", "Drafts", "垃圾邮件"} {
				if st, e := c.Select(alias, true); e == nil && st != nil {
					if int(st.Messages) > bestCount {
						bestCount = int(st.Messages)
						bestMbox = alias
					}
				}
			}
			if bestMbox != "" {
				// Re-select the winner (the last Select may have been a different folder).
				_, _ = c.Select(bestMbox, true)
				mbox = bestMbox
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("select %s: %w", mbox, err)
		}
	}

	criteria := &imap.SearchCriteria{}
	if unreadOnly && mbox == "INBOX" {
		// Only filter unread in the inbox; sent folders show all.
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}
	if !since.IsZero() {
		criteria.Since = since
	}
	if !before.IsZero() {
		criteria.Before = before
	}
	seqs, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(seqs) == 0 {
		return nil, nil
	}
	// Take most recent `limit` (Search returns ascending; newest is last).
	if limit > 0 && len(seqs) > limit {
		seqs = seqs[len(seqs)-limit:]
	}
	// Newest-first.
	for i, j := 0, len(seqs)-1; i < j; i, j = i+1, j-1 {
		seqs[i], seqs[j] = seqs[j], seqs[i]
	}
	return fetchMessages(c, seqs, withBody)
}

// imapSearch runs a server-side IMAP SEARCH narrowed by from/subject header
// substrings and/or an internal-date range, then fetches matches. Empty filters
// are omitted, so a date-only search returns every message in the window.
func imapSearch(ctx context.Context, cfg config.IMAPConfig, mailbox, from, subject string, limit int, since, before time.Time) ([]EmailMessage, error) {
	c, err := imapConnect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout() }()

	mbox := strings.TrimSpace(mailbox)
	if mbox == "" {
		mbox = "INBOX"
	}
	if _, err := c.Select(mbox, true); err != nil {
		if mbox != "INBOX" {
			bestMbox := ""
			bestCount := -1
			for _, alias := range []string{"已发送", "已发送邮件", "Sent Messages", "Sent", "[Gmail]/Sent Mail"} {
				if st, e := c.Select(alias, true); e == nil && st != nil {
					if int(st.Messages) > bestCount {
						bestCount = int(st.Messages)
						bestMbox = alias
					}
				}
			}
			if bestMbox != "" {
				_, _ = c.Select(bestMbox, true)
				mbox = bestMbox
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("select %s: %w", mbox, err)
		}
	}
	// go-imap's SearchCriteria.Header is a map[string][]string of header fields
	// to match (IMAP SEARCH HEADER). Build it from whichever of from/subject the
	// caller supplied; date bounds go on Since/Before (internal date = receive).
	criteria := &imap.SearchCriteria{Header: map[string][]string{}}
	if strings.TrimSpace(from) != "" {
		criteria.Header["From"] = []string{from}
	}
	if strings.TrimSpace(subject) != "" {
		criteria.Header["Subject"] = []string{subject}
	}
	if !since.IsZero() {
		criteria.Since = since
	}
	if !before.IsZero() {
		criteria.Before = before
	}
	seqs, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(seqs) == 0 {
		return nil, nil
	}
	if limit > 0 && len(seqs) > limit {
		seqs = seqs[len(seqs)-limit:]
	}
	for i, j := 0, len(seqs)-1; i < j; i, j = i+1, j-1 {
		seqs[i], seqs[j] = seqs[j], seqs[i]
	}
	return fetchMessages(c, seqs, false)
}

// fetchMessages FETCHes ENVELOPE + the full body for the given sequence numbers,
// parsing each via go-message for correct MIME/charset handling. Body preview is
// the first ~500 chars of the text/plain part (or text/html stripped, fallback).
func fetchMessages(c *client.Client, seqs []uint32, withBody bool) ([]EmailMessage, error) {
	seqset := new(imap.SeqSet)
	for _, s := range seqs {
		seqset.AddNum(s)
	}
	messages := make(chan *imap.Message, len(seqs))
	items := []imap.FetchItem{imap.FetchEnvelope}
	if withBody {
		items = append(items, imap.FetchItem("BODY[]"))
	}
	if err := c.Fetch(seqset, items, messages); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	var out []EmailMessage
	for msg := range messages {
		if msg == nil {
			continue
		}
		out = append(out, parseMessage(msg))
	}
	// IMAP FETCH responses arrive in server-dependent order, not necessarily by
	// sequence number or date. Sort newest-first by envelope date so the dock and
	// the agent text view both show the most recent message on top. Messages
	// without a parseable date (rawDate zero) sink to the bottom; ties keep a
	// stable order.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].rawDate, out[j].rawDate
		if a.IsZero() && b.IsZero() {
			return false
		}
		if a.IsZero() {
			return false // a has no date → a goes after b
		}
		if b.IsZero() {
			return true
		}
		return a.After(b)
	})
	return out, nil
}

// parseMessage reads the raw body via go-message/mail for RFC 2047 header
// decoding + multipart body extraction. Falls back to envelope-only if the body
// can't be parsed (so a malformed message still returns its headers).
func parseMessage(msg *imap.Message) EmailMessage {
	m := EmailMessage{}
	if env := msg.Envelope; env != nil {
		m.From = formatAddresses(env.From)
		m.To = formatAddresses(env.To)
		m.Subject = decodeRFC2047(env.Subject)
		if env.Date != (time.Time{}) {
			m.rawDate = env.Date
			// RFC3339 (ISO 8601) is parsed reliably by JS's new Date() — the prior
			// RFC1123Z ("30 Jul 26 09:15 +0800") format frequently failed to parse
			// in the frontend, leaving the dock's date column blank/abbreviated.
			m.Date = env.Date.Format(time.RFC3339)
		}
	}
	// Read the raw message bytes and parse MIME for a body preview + attachments.
	var bodyBytes []byte
	section := &imap.BodySectionName{}
	r := msg.GetBody(section)
	if r != nil {
		bodyBytes, _ = io.ReadAll(r)
	}
	if len(bodyBytes) > 0 {
		m.Preview = extractTextPreview(bodyBytes)
		m.Attachments = extractAttachmentMeta(bodyBytes)
	}
	return m
}

// extractTextPreview parses a raw MIME message and returns the first ~500 chars
// of its text/plain part (preferred) or text/html (tags stripped, fallback).
func extractTextPreview(raw []byte) string {
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		// Not a parseable MIME message — return a raw slice.
		return truncatePreview(strings.TrimSpace(string(raw)), 500)
	}
	defer mr.Close()
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		ct := part.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "text/plain") {
			data, _ := io.ReadAll(part.Body)
			return truncatePreview(strings.TrimSpace(decodeBodyCharset(data, ct)), 2000)
		}
	}
	// No text/plain found — fall back to first text part (html) stripped.
	mr2, _ := mail.CreateReader(strings.NewReader(string(raw)))
	if mr2 != nil {
		defer mr2.Close()
		for {
			part, err := mr2.NextPart()
			if err != nil {
				break
			}
			ct := part.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "text/") {
				data, _ := io.ReadAll(part.Body)
				return truncatePreview(strings.TrimSpace(stripHTMLText(decodeBodyCharset(data, ct))), 500)
			}
		}
	}
	return ""
}

// decodeBodyCharset converts a mail body part from its declared charset to
// UTF-8. go-message's mail.Reader undoes Content-Transfer-Encoding (base64/QP)
// but does NOT transcode charsets, so a GBK-encoded body (common in 139.com,
// QQ Mail, etc.) would arrive as raw GBK bytes and render as mojibake. We
// parse the charset from the Content-Type header and run it through the same
// x/text decoder used for RFC 2047 headers. UTF-8 / unknown charsets pass
// through unchanged (UTF-8 needs no conversion; unknown is better left raw than
// mis-decoded).
func decodeBodyCharset(data []byte, contentType string) string {
	charset := parseCharsetFromContentType(contentType)
	if charset == "" || strings.EqualFold(charset, "utf-8") || strings.EqualFold(charset, "us-ascii") {
		return string(data) // already UTF-8 or ASCII — no conversion needed
	}
	dec := charsetToDecoder(charset)
	if dec == nil {
		return string(data) // unrecognized charset — leave raw (don't guess wrong)
	}
	utf8, _, err := transform.String(dec, string(data))
	if err != nil {
		return string(data) // decode failed — fall back to raw bytes
	}
	return utf8
}

// parseCharsetFromContentType extracts the charset parameter value from a
// Content-Type header like "text/plain; charset=gbk" → "gbk". Returns "" when
// absent. Quotes are stripped.
func parseCharsetFromContentType(ct string) string {
	// Split on ";" and look for a charset=... part.
	for _, part := range strings.Split(ct, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "charset=") {
			val := strings.TrimSpace(part[len("charset="):])
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}

// extractAttachmentMeta parses a raw MIME message and returns metadata for
// attachments (filename + size). Does NOT return the attachment content.
func extractAttachmentMeta(raw []byte) []EmailAttachment {
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		return nil
	}
	defer mr.Close()
	var out []EmailAttachment
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		cd := part.Header.Get("Content-Disposition")
		if !strings.HasPrefix(cd, "attachment") {
			continue
		}
		name := ""
		// Parse filename from Content-Disposition
		for _, seg := range strings.Split(cd, ";") {
			seg = strings.TrimSpace(seg)
			if strings.HasPrefix(seg, "filename=") {
				name = strings.Trim(strings.TrimPrefix(seg, "filename="), "\"")
			}
		}
		if name == "" {
			continue
		}
		data, _ := io.ReadAll(part.Body)
		out = append(out, EmailAttachment{Name: name, Size: len(data)})
	}
	return out
}

// downloadAttachments fetches recent messages and saves their attachments to dir.
// Returns the count of saved files.
func downloadAttachments(ctx context.Context, cfg config.IMAPConfig, limit int, unreadOnly bool, since, before time.Time, dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	c, err := imapConnect(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Logout() }()

	if _, err := c.Select("INBOX", true); err != nil {
		return 0, fmt.Errorf("select inbox: %w", err)
	}

	criteria := &imap.SearchCriteria{}
	if unreadOnly {
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}
	if !since.IsZero() {
		criteria.Since = since
	}
	if !before.IsZero() {
		criteria.Before = before
	}
	seqs, err := c.Search(criteria)
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	if len(seqs) == 0 {
		return 0, nil
	}
	if limit > 0 && len(seqs) > limit {
		seqs = seqs[len(seqs)-limit:]
	}

	seqset := new(imap.SeqSet)
	for _, s := range seqs {
		seqset.AddNum(s)
	}
	messages := make(chan *imap.Message, len(seqs))
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchItem("BODY[]")}
	if err := c.Fetch(seqset, items, messages); err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	saved := 0
	for msg := range messages {
		if msg == nil {
			continue
		}
		section := &imap.BodySectionName{}
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		bodyBytes, _ := io.ReadAll(r)
		if len(bodyBytes) == 0 {
			continue
		}
		files := saveAttachmentsFromRaw(bodyBytes, dir)
		saved += files
	}
	return saved, nil
}

// saveAttachmentsFromRaw extracts attachments from a raw MIME message and saves
// them to dir. Returns the count of saved files.
func saveAttachmentsFromRaw(raw []byte, dir string) int {
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		return 0
	}
	defer mr.Close()
	saved := 0
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		cd := part.Header.Get("Content-Disposition")
		if !strings.HasPrefix(cd, "attachment") {
			continue
		}
		name := ""
		for _, seg := range strings.Split(cd, ";") {
			seg = strings.TrimSpace(seg)
			if strings.HasPrefix(seg, "filename=") {
				name = strings.Trim(strings.TrimPrefix(seg, "filename="), "\"")
			}
		}
		if name == "" {
			continue
		}
		// SECURITY: attachment names come straight from the sender's MIME
		// Content-Disposition header, which an attacker fully controls. A
		// name like "../../evil.exe" would let a malicious email write
		// anywhere on disk via filepath.Join. Reduce to a bare filename and
		// reject anything that still looks pathy after Base. See audit A5.
		name = filepath.Base(name)
		if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		data, _ := io.ReadAll(part.Body)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			continue
		}
		saved++
	}
	return saved
}

// sensitiveAttachmentSubdirs lists path segments that, if they appear in a
// save_attachments target, mark it as too dangerous to write attacker-controlled
// email attachment bytes into. The attachment filename is set by the sender
// (Content-Disposition), so a path like ~/.ssh/ lets a malicious email overwrite
// authorized_keys.
var sensitiveAttachmentSubdirs = []string{
	".ssh", ".aws", ".gnupg", ".config",
	"Windows", "System32", "Startup", "Start Menu",
	"etc", "bin", "sbin", "usr/bin",
}

// guardAttachmentDir rejects save_attachments targets that point at or into
// well-known sensitive directories. It is a denylist guard, not a full workspace
// confine (which would require boot-time root injection); it blocks the
// known-dangerous paths an injected prompt would target. See security review #3.
func guardAttachmentDir(dir string) error {
	dir = filepath.Clean(dir)
	lower := strings.ToLower(dir)
	for _, seg := range sensitiveAttachmentSubdirs {
		if strings.Contains(lower, strings.ToLower(seg)) {
			return fmt.Errorf("save_attachments directory %q is in or under a sensitive location (%s); choose a workspace subdirectory instead", dir, seg)
		}
	}
	return nil
}

// formatAddresses renders an imap.Address slice as "Name <addr>, Name2 <addr2>".
func formatAddresses(addrs []*imap.Address) string {
	var parts []string
	for _, a := range addrs {
		name := strings.TrimSpace(strings.Trim(a.PersonalName, "\""))
		name = decodeRFC2047(name)
		mailbox := a.MailboxName
		host := a.HostName
		if mailbox == "" {
			continue
		}
		addr := mailbox + "@" + host
		if name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", name, addr))
		} else {
			parts = append(parts, addr)
		}
	}
	return strings.Join(parts, ", ")
}

// --- tool types (unchanged surface, upgraded impl) -------------------------

type emailReadTool struct{}

func (emailReadTool) Name() string { return "email_read" }

func (emailReadTool) Description() string {
	return "Read recent emails from the configured IMAP inbox ([cowork.imap] host/port/username/password_env). Returns the most recent messages with from/to/subject/date/body-preview (RFC 2047 subjects and multipart bodies handled correctly). Set unread_only=true for unread only, limit to cap the count (default 30). Requires IMAP config; returns a config error if unset."
}

func (emailReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "limit":{"type":"integer","description":"Max messages to return (default 30)"},
  "unread_only":{"type":"boolean","description":"Only unread messages (default false)"},
  "account":{"type":"string","description":"邮箱账号名（[[cowork.email_accounts]] 的 name），留空=默认账号"},
  "since":{"type":"string","description":"只读这个日期之后的邮件；绝对日期 2026-06-01 或相对 7d/1w/1m（近N天/周/月）"},
  "before":{"type":"string","description":"只读这个日期之前的邮件；格式同 since"},
  "save_attachments":{"type":"string","description":"附件下载目录（绝对路径）；留空=只列出附件名不下载"},
  "mailbox":{"type":"string","description":"要读取的邮箱文件夹，留空=INBOX 收件箱；填 IMAP 标准名如 Sent（已发送）、Drafts（草稿）、Trash（已删除）、Junk（垃圾邮件）等"}
},
"required":[]
}`)
}

func (emailReadTool) ReadOnly() bool { return false } // save_attachments writes files

func (emailReadTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Limit           int    `json:"limit"`
		UnreadOnly      bool   `json:"unread_only"`
		Account         string `json:"account"`
		Mailbox         string `json:"mailbox"`
		Since           string `json:"since"`
		Before          string `json:"before"`
		SaveAttachments string `json:"save_attachments"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	since, before, err := parseDateRange(p.Since, p.Before)
	if err != nil {
		return "", err
	}
	cfg, err := resolveIMAP(p.Account)
	if err != nil {
		return "", err
	}
	// p.Mailbox is "" for the default INBOX; "Sent" exposes the sent view.
	mbox := strings.TrimSpace(p.Mailbox)
	if mbox == "" {
		mbox = "INBOX"
	}
	msgs, err := imapRead(ctx, cfg, mbox, p.Limit, p.UnreadOnly, true, since, before)
	if err != nil {
		return "", friendlyEmailErr(p.Account, err)
	}
	if len(msgs) == 0 {
		return "no messages", nil
	}
	// Download attachments if requested.
	if p.SaveAttachments != "" {
		// SECURITY: save_attachments comes straight from the agent and controls
		// where attachment bytes (attacker-controlled via email) get written.
		// Without this check an injected prompt could direct attachments to
		// ~/.ssh/authorized_keys or similar sensitive paths. ConfineWriters does
		// not cover email_read (it's not a generic writer), so we guard here.
		// See security review finding #3. Full workspace confine via boot
		// injection is tracked separately; this blocks the known-dangerous
		// targets.
		if err := guardAttachmentDir(p.SaveAttachments); err != nil {
			return "", err
		}
		saved, err := downloadAttachments(ctx, cfg, p.Limit, p.UnreadOnly, since, before, p.SaveAttachments)
		if err != nil {
			return "", fmt.Errorf("download attachments: %w", err)
		}
		return formatMessages(msgs) + fmt.Sprintf("\nsaved %d attachment(s) to %s", saved, p.SaveAttachments), nil
	}
	return formatMessages(msgs), nil
}

type emailSearchTool struct{}

func (emailSearchTool) Name() string { return "email_search" }

func (emailSearchTool) Description() string {
	return "Search emails by from-address (server-side IMAP SEARCH FROM). Returns matching messages with envelopes + previews. Use to find mail from a specific sender. Requires [cowork.imap] config."
}

func (emailSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "from":{"type":"string","description":"发件人地址子串（IMAP SEARCH FROM），可选"},
  "subject":{"type":"string","description":"主题子串（IMAP SEARCH SUBJECT），可选"},
  "limit":{"type":"integer","description":"Max results (default 30)"},
  "account":{"type":"string","description":"邮箱账号名（[[cowork.email_accounts]] 的 name），留空=默认账号"},
  "since":{"type":"string","description":"只搜这个日期之后的邮件；绝对日期 2026-06-01 或相对 7d/1w/1m"},
  "before":{"type":"string","description":"只搜这个日期之前的邮件；格式同 since"},
  "mailbox":{"type":"string","description":"要搜索的邮箱文件夹，留空=INBOX 收件箱；填 IMAP 标准名如 Sent（已发送）、Drafts（草稿）、Trash（已删除）、Junk（垃圾邮件）等"}
},
"required":[]
}`)
}

func (emailSearchTool) ReadOnly() bool { return true }

func (emailSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		From    string `json:"from"`
		Subject string `json:"subject"`
		Limit   int    `json:"limit"`
		Account string `json:"account"`
		Mailbox string `json:"mailbox"`
		Since   string `json:"since"`
		Before  string `json:"before"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(p.From) == "" && strings.TrimSpace(p.Subject) == "" && strings.TrimSpace(p.Since) == "" && strings.TrimSpace(p.Before) == "" {
		return "", errors.New("at least one of from / subject / since / before is required")
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	since, before, err := parseDateRange(p.Since, p.Before)
	if err != nil {
		return "", err
	}
	cfg, err := resolveIMAP(p.Account)
	if err != nil {
		return "", err
	}
	msgs, err := imapSearch(ctx, cfg, p.Mailbox, p.From, p.Subject, p.Limit, since, before)
	if err != nil {
		return "", friendlyEmailErr(p.Account, err)
	}
	if len(msgs) == 0 {
		return "no matching messages", nil
	}
	return formatMessages(msgs), nil
}

func formatMessages(msgs []EmailMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "%d. from: %s\n   to: %s\n   date: %s\n   subject: %s\n   preview: %s\n",
			i+1, m.From, m.To, m.Date, m.Subject, m.Preview)
		if len(m.Attachments) > 0 {
			fmt.Fprintf(&b, "   attachments: ")
			for j, a := range m.Attachments {
				if j > 0 {
					fmt.Fprintf(&b, ", ")
				}
				fmt.Fprintf(&b, "%s (%dKB)", a.Name, (a.Size+1023)/1024)
			}
			fmt.Fprintf(&b, "\n")
		}
		fmt.Fprintf(&b, "\n")
	}
	// SECURITY: every field above (From, Subject, Preview, attachment names)
	// comes from the email sender, who fully controls the bytes. A malicious
	// email can embed prompt-injection text ("ignore prior instructions…") in
	// any of them. Wrap the whole block so the model treats it as DATA. This
	// is the same defense web_fetch / rag_search / browser use; email is the
	// one untrusted channel that was missing it. See audit finding A4.
	return WrapUntrusted("email", b.String())
}

func truncatePreview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// osGetenv wraps os.Getenv (kept so this file's env access is localized).
func osGetenv(key string) string { return os.Getenv(key) }

// mailHeaderFromFields is unused (we use the Header map directly); kept as a
// compile guard against an accidental unused import.
var _ = mail.CreateReader
