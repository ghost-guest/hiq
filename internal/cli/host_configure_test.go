package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/remotehost"
	"github.com/zzycxz/hiq/internal/secret"
)

// TestHostConfigureMirrorsModelConfig verifies the desktop-push path on a fresh
// host: provider entries land in the user config, the key lands in the secret
// store (and the running process env), and hasUsableProvider flips so a second
// push is a no-op.
func TestHostConfigureMirrorsModelConfig(t *testing.T) {
	isolateCLIConfigHome(t)

	if hasUsableProvider() {
		t.Fatal("fresh isolated host should not report a usable provider")
	}
	res, err := configureRemote(remotehost.ConfigureParams{
		DefaultModel: "pushed/pushed-model",
		Providers: []remotehost.ProviderSnapshot{{
			Name: "pushed", Kind: "acp-test-provider",
			APIKeyEnv: "HIQ_PUSHED_KEY", APIKey: "pushed-key",
			Models: []string{"pushed-model"},
		}},
	})
	if err != nil || !res.Configured {
		t.Fatalf("configureRemote = %+v, %v", res, err)
	}

	if v, ok, _ := secret.New(secret.DefaultPath()).Get("HIQ_PUSHED_KEY"); !ok || v != "pushed-key" {
		t.Fatalf("secret store key = %q, %v", v, ok)
	}
	if os.Getenv("HIQ_PUSHED_KEY") != "pushed-key" {
		t.Fatal("configure should apply the key to the running process env")
	}
	data, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`name = "pushed"`, `api_key_env = "HIQ_PUSHED_KEY"`, `models = ["pushed-model"]`, `default_model = "pushed/pushed-model"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("user config missing %q:\n%s", want, text)
		}
	}
	if !hasUsableProvider() {
		t.Fatal("host should report a usable provider after configure")
	}

	// A second push must not overwrite (already configured).
	res2, err := configureRemote(remotehost.ConfigureParams{DefaultModel: "other/x"})
	if err != nil || !res2.AlreadyConfigured || res2.Configured {
		t.Fatalf("second configureRemote = %+v, %v", res2, err)
	}
}
