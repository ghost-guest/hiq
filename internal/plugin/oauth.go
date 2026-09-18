// oauth.go — upgrade spec 3-7③: OAuth 2.0 PKCE flow for MCP HTTP servers.
// Many remote MCP servers (Atlassian, Notion, Linear…) require OAuth instead
// of a static API key. The flow: discover the server's authorization endpoint
// (RFC 9728 metadata or manual config), open a browser, run a local callback
// server on a random port, exchange the code with PKCE, and store the token
// in the config directory for reuse. The httpTransport injects it as a
// Bearer header on every request and refreshes when it expires.
package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OAuthConfig describes one server's OAuth endpoints. Manually configured
// (users copy from the server's docs); auto-discovery via RFC 9728 is a
// future addition.
type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	AuthURL      string `json:"auth_url"`
	TokenURL     string `json:"token_url"`
	Scopes       string `json:"scopes,omitempty"`
	RedirectPort int    `json:"redirect_port,omitempty"` // 0 = random
}

// OAuthToken is the stored credential.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
}

// valid reports whether the token is usable right now (with a 30s clock skew
// margin so we refresh slightly before actual expiry).
func (t *OAuthToken) valid() bool {
	return t != nil && t.AccessToken != "" &&
		(t.ExpiresAt.IsZero() || time.Now().Add(30*time.Second).Before(t.ExpiresAt))
}

// tokenStore persists OAuth tokens per server name under
// <configDir>/mcp-oauth/<sanitized-name>.json.
type tokenStore struct {
	dir string
	mu  sync.Mutex
}

func newTokenStore(configDir string) *tokenStore {
	return &tokenStore{dir: filepath.Join(configDir, "mcp-oauth")}
}

func (ts *tokenStore) path(server string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, server)
	return filepath.Join(ts.dir, name+".json")
}

func (ts *tokenStore) load(server string) *OAuthToken {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b, err := os.ReadFile(ts.path(server))
	if err != nil {
		return nil
	}
	var t OAuthToken
	if json.Unmarshal(b, &t) != nil {
		return nil
	}
	return &t
}

func (ts *tokenStore) save(server string, t *OAuthToken) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := os.MkdirAll(ts.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(ts.path(server), b, 0o600)
}

// pkcePair generates a code_verifier + S256 code_challenge.
func pkcePair() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	// S256: BASE64URL(SHA256(verifier))
	sum := sha256Sum([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum)
	return
}

// RunPKCEFlow performs the full authorization-code + PKCE exchange. It blocks
// until the user completes the browser flow (or the context cancels).
func RunPKCEFlow(ctx context.Context, cfg OAuthConfig, serverName string, configDir string, openBrowser func(url string) error) (*OAuthToken, error) {
	store := newTokenStore(configDir)

	// Check for a refreshable token first.
	if existing := store.load(serverName); existing != nil && existing.RefreshToken != "" {
		if refreshed, err := refresh(ctx, cfg, existing.RefreshToken); err == nil {
			_ = store.save(serverName, refreshed)
			return refreshed, nil
		}
		// Fall through to a fresh flow when refresh fails.
	}

	verifier, challenge := pkcePair()
	port := cfg.RedirectPort
	if port == 0 {
		port = 0 // OS assigns
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("oauth callback listen: %w", err)
	}
	defer ln.Close()
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	// Build the authorize URL.
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {callbackURL},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if cfg.Scopes != "" {
		q.Set("scope", cfg.Scopes)
	}
	authURL := cfg.AuthURL + "?" + q.Encode()

	// Run the callback server.
	codeCh := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("missing code parameter"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h2>Authorization complete</h2><p>You can close this tab and return to hiq.</p></body></html>`))
		codeCh <- code
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second); defer cancel(); _ = srv.Shutdown(shutdownCtx) }()

	// Open the browser.
	if openBrowser != nil {
		if err := openBrowser(authURL); err != nil {
			return nil, fmt.Errorf("open browser: %w (authorize manually at %s)", err, authURL)
		}
	}

	// Wait for the callback or ctx cancel.
	var code string
	select {
	case code = <-codeCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("oauth flow timed out after 5 minutes")
	}

	// Exchange the code for tokens.
	tok, err := exchange(ctx, cfg, code, verifier, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	_ = store.save(serverName, tok)
	return tok, nil
}

// exchange trades the authorization code for tokens.
func exchange(ctx context.Context, cfg OAuthConfig, code, verifier, redirectURI string) (*OAuthToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}
	return tokenRequest(ctx, cfg.TokenURL, form)
}

// refresh uses a refresh token to get a new access token.
func refresh(ctx context.Context, cfg OAuthConfig, refreshToken string) (*OAuthToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.ClientID},
	}
	return tokenRequest(ctx, cfg.TokenURL, form)
}

func tokenRequest(ctx context.Context, tokenURL string, form url.Values) (*OAuthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token endpoint %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	tok := &OAuthToken{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		TokenType:    body.TokenType,
	}
	if body.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// sha256Sum is a small helper to avoid importing crypto/sha256 at the top
// for one call.
func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}
