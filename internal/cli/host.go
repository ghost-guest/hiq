package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zzycxz/fairpeer/internal/boot"
	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/control"
	"github.com/zzycxz/fairpeer/internal/event"
	"github.com/zzycxz/fairpeer/internal/i18n"
	"github.com/zzycxz/fairpeer/internal/remotehost"
	"github.com/zzycxz/fairpeer/internal/secret"
)

// hostCommand runs fairpeer as a remote-workspace host: a stdio JSON-RPC server
// the desktop drives when a tab's workspace lives on this machine (spawned via
// wsl.exe / docker exec / ssh). One process serves many sessions; the desktop
// detects stale builds via host/hello and re-provisions the binary.
//
// stdin/stdout are the JSON-RPC channel — all diagnostics go to stderr.
func hostCommand(args []string, version string) int {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	listen := fs.String("listen", "", "serve over TCP on this address (Server connection kind) instead of stdio")
	token := fs.String("token", "", "TCP mode: shared secret required from each client (mandatory with -listen)")
	useTLS := fs.Bool("tls", false, "TCP mode: upgrade the listener to TLS with a self-signed certificate (pinned by the desktop on first connect)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	home, _ := os.UserHomeDir()
	factory := &hostFactory{}
	info := remotehost.HelloInfo{
		Version:    version,
		Goos:       runtime.GOOS,
		Arch:       runtime.GOARCH,
		Home:       home,
		ConfigRoot: filepath.Dir(config.UserConfigPath()),
	}
	var err error
	if strings.TrimSpace(*listen) != "" {
		certDir := ""
		if *useTLS {
			certDir = filepath.Join(filepath.Dir(config.UserConfigPath()), "remote-host-tls")
		}
		err = remotehost.ListenServe(ctx, *listen, *token, certDir, factory, info, configureRemote, hasUsableProvider)
	} else {
		err = remotehost.Serve(ctx, os.Stdin, os.Stdout, factory, info, configureRemote, hasUsableProvider)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	return 0
}

// hostFactory assembles one remote session's controller, mirroring the
// desktop's buildTabController: load the root's config, resolve the model with
// fallback, boot with the per-project session dir and presentation sidecar on.
type hostFactory struct{}

func (f *hostFactory) NewController(ctx context.Context, p remotehost.SessionNewParams, sink event.Sink) (*control.Controller, error) {
	root := filepath.Clean(strings.TrimSpace(p.Cwd))
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = cfg.DefaultModel
	}
	if resolved, fallback, ok := cfg.ResolveModelWithFallback(model); ok {
		_ = fallback
		model = resolved
	} else if model != "" {
		model = ""
	}
	var effortOverride *string
	if e := strings.TrimSpace(p.Effort); e != "" {
		effortOverride = &e
	}
	var profile *config.Profile
	if name := strings.TrimSpace(p.Profile); name != "" && !strings.EqualFold(name, config.ProfileDev) {
		if pr, perr := cfg.ResolveProfile(name); perr == nil {
			profile = pr
		}
	}
	return boot.Build(ctx, boot.Options{
		StatsSource:    "cli",
		Model:          model,
		RequireKey:     false,
		Sink:           sink,
		Stderr:         os.Stderr,
		WorkspaceRoot:  root,
		SessionDir:     config.ProjectSessionDir(root),
		EffortOverride: effortOverride,
		Profile:        profile,
		Present:        true,
	})
}

// hasUsableProvider reports whether this side already has at least one provider
// whose api_key_env resolves (process env, or the secret store — the store is
// only pumped into env once per process, so a key the desktop just pushed is
// checked straight from the store). host/configure uses it to avoid overwriting
// a remote the user configured by hand.
func hasUsableProvider() bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	storeKeys := map[string]bool{}
	if keys, err := secret.New(secret.DefaultPath()).Keys(); err == nil {
		for _, k := range keys {
			storeKeys[k] = true
		}
	}
	for _, p := range cfg.Providers {
		if p.Configured() {
			return true
		}
		if p.APIKeyEnv != "" && storeKeys[p.APIKeyEnv] {
			return true
		}
	}
	return false
}

// configureRemote mirrors the desktop's model configuration into this side's
// user config + secret store, only when nothing usable exists yet.
func configureRemote(p remotehost.ConfigureParams) (remotehost.ConfigureResult, error) {
	if hasUsableProvider() {
		return remotehost.ConfigureResult{AlreadyConfigured: true}, nil
	}
	if len(p.Providers) == 0 {
		return remotehost.ConfigureResult{}, nil
	}
	path := config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return remotehost.ConfigureResult{}, err
	}
	store := secret.New(secret.DefaultPath())
	var b strings.Builder
	b.WriteString("\n# Mirrored from the desktop's model configuration (first remote connect).\n")
	for _, prov := range p.Providers {
		name := strings.TrimSpace(prov.Name)
		if name == "" {
			continue
		}
		apiKeyEnv := strings.TrimSpace(prov.APIKeyEnv)
		if apiKeyEnv == "" {
			apiKeyEnv = providerKeyEnvName(name)
		}
		b.WriteString("\n[[providers]]\n")
		b.WriteString("name = " + tomlString(name) + "\n")
		if k := strings.TrimSpace(prov.Kind); k != "" {
			b.WriteString("kind = " + tomlString(k) + "\n")
		}
		if u := strings.TrimSpace(prov.BaseURL); u != "" {
			b.WriteString("base_url = " + tomlString(u) + "\n")
		}
		b.WriteString("api_key_env = " + tomlString(apiKeyEnv) + "\n")
		if len(prov.Models) > 0 {
			b.WriteString("models = [")
			for i, m := range prov.Models {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(tomlString(m))
			}
			b.WriteString("]\n")
		}
		if prov.ContextWindow > 0 {
			fmt.Fprintf(&b, "context_window = %d\n", prov.ContextWindow)
		}
		if prov.Vision {
			b.WriteString("vision = true\n")
		}
		if key := strings.TrimSpace(prov.APIKey); key != "" {
			if err := store.Set(apiKeyEnv, key); err != nil {
				return remotehost.ConfigureResult{}, err
			}
			// Apply to the running process too: the once-per-process secret→env
			// pump already ran, and session builds resolve keys from the env.
			os.Setenv(apiKeyEnv, key)
		}
	}
	if dm := strings.TrimSpace(p.DefaultModel); dm != "" {
		b.WriteString("\n[agent]\ndefault_model = " + tomlString(dm) + "\n")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return remotehost.ConfigureResult{}, err
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return remotehost.ConfigureResult{}, err
	}
	return remotehost.ConfigureResult{Configured: true}, nil
}

func tomlString(s string) string {
	return fmt.Sprintf("%q", s)
}

// providerKeyEnvName derives the secret-store key for a mirrored provider:
// FAIRPEER_<UPPER_SNAKE_NAME>_API_KEY.
func providerKeyEnvName(name string) string {
	var b strings.Builder
	b.WriteString("FAIRPEER_")
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	b.WriteString("_API_KEY")
	return b.String()
}
