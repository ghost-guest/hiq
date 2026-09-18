package netdev

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/netdev/transport"
)

// httpfp.go — F3: opt-in application fingerprint (spec §4.4, default OFF).
// The polite constitution: ONE standard `GET /` with a normal browser UA per
// port; TLS ports get one ordinary handshake (cert read rides the same
// request — InsecureSkipVerify only means we don't reject self-signed certs
// while READING them; we never verify, we fingerprint). No path
// enumeration, no retries, no probes disguised as browsers.

// HTTPFingerprint is what one polite request learned.
type HTTPFingerprint struct {
	Title   string `json:"title,omitempty"`
	Server  string `json:"server,omitempty"`
	CertCN  string `json:"cert_cn,omitempty"`
	CertSAN string `json:"cert_san,omitempty"`
}

// httpFingerprintPorts: the ports this pass looks at.
var httpFingerprintPorts = map[int]bool{80: true, 443: true, 8080: true, 8443: true}

const httpfpUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Hiq-Ops/1.0"

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.{0,200}?)</title>`)

// httpFingerprint sends the single request. dialer keeps the probe on the
// discovery path (direct or through the hop's tunnel). nil = nothing learned.
func httpFingerprint(ctx context.Context, dialer transport.Dialer, ip string, port int, useTLS bool) *HTTPFingerprint {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	client := &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 3 * time.Second,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true, ServerName: ip},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s://%s:%d/", scheme, ip, port), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", httpfpUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	fp := &HTTPFingerprint{Server: res.Header.Get("Server")}
	if res.TLS != nil && len(res.TLS.PeerCertificates) > 0 {
		cert := res.TLS.PeerCertificates[0]
		fp.CertCN = cert.Subject.CommonName
		if len(cert.DNSNames) > 0 {
			fp.CertSAN = strings.Join(cert.DNSNames, ",")
		}
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	if m := titleRe.FindStringSubmatch(string(body)); m != nil {
		fp.Title = strings.TrimSpace(htmlUnescape(m[1]))
	}
	if fp.Title == "" && fp.Server == "" && fp.CertCN == "" {
		return nil
	}
	return fp
}
