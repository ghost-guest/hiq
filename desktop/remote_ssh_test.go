package main

// TestSSHTransportE2E drives the real sshTransport against a live sshd. It is
// opt-in (FP_SSH_E2E="user:password@host:port") because CI has no target:
//
//	docker run -d --name fp-ssh-smoke -p 127.0.0.1:2222:22 alpine sh -c \
//	  "apk add --no-cache openssh && ssh-keygen -A && echo 'root:fp12345' | chpasswd && \
//	   echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config && /usr/sbin/sshd -D"
//	FP_SSH_E2E=root:fp12345@127.0.0.1:2222 go test -run TestSSHTransportE2E -count=1
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/remotehost"
)

func TestSSHTransportE2E(t *testing.T) {
	spec := strings.TrimSpace(os.Getenv("FP_SSH_E2E"))
	if spec == "" {
		t.Skip("FP_SSH_E2E not set (user:password@host:port)")
	}
	credsPart, target, ok := strings.Cut(spec, "@")
	if !ok {
		t.Fatal("spec must be user:password@host:port")
	}
	user, password, ok := strings.Cut(credsPart, ":")
	if !ok {
		t.Fatal("spec must be user:password@host:port")
	}
	host, port, _ := strings.Cut(target, ":")
	if port == "" {
		port = "22"
	}

	ref := RemoteRef{Kind: "ssh", Target: sshTarget(host, port), User: user}
	tr := &sshTransport{creds: &sshCredentials{
		Host: host, Port: port, User: user,
		AuthMethod: "password", Password: password,
	}}

	// Trust flow: an unknown key must be REJECTED by the transport (no silent
	// TOFU), then accepted once sshTrustHostKey has pinned it.
	managed := filepath.Join(t.TempDir(), "known_hosts")
	untrusted := &sshTransport{creds: tr.creds, managedPath: managed}
	ctx0, cancel0 := context.WithTimeout(context.Background(), time.Minute)
	if _, _, _, err := untrusted.Dial(ctx0, ref); err == nil {
		cancel0()
		t.Fatal("dial with untrusted host key should fail (strict prompt)")
	}
	cancel0()
	if err := sshTrustHostKey(host, port, managed); err != nil {
		t.Fatalf("trust host key: %v", err)
	}
	tr.managedPath = managed

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stdin, stdout, proc, err := tr.Dial(ctx, ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	link := newRemoteHostLink(ctx, stdin, stdout, proc)
	defer link.close()

	var hello remotehost.HelloResult
	if err := link.call(ctx, "host/hello", map[string]any{}, &hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello.Goos != "linux" {
		t.Fatalf("hello goos = %s, want linux", hello.Goos)
	}
	var cfgRes remotehost.ConfigureResult
	if err := link.call(ctx, "host/configure", remotehost.ConfigureParams{
		DefaultModel: "pushed/fake-model",
		Providers: []remotehost.ProviderSnapshot{{
			Name: "pushed", Kind: "openai", BaseURL: "http://127.0.0.1:9/v1",
			APIKeyEnv: "HIQ_PUSHED_KEY", APIKey: "sk-smoke", Models: []string{"fake-model"},
		}},
	}, &cfgRes); err != nil || !cfgRes.Configured {
		t.Fatalf("configure: %v %+v", err, cfgRes)
	}

	var newRes remotehost.SessionNewResult
	if err := link.call(ctx, "session/new", remotehost.SessionNewParams{SessionID: "smoke", Cwd: "/etc"}, &newRes); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if !strings.HasPrefix(newRes.SessionPath, "/root/.config/hiq/") && !strings.HasPrefix(newRes.SessionPath, "/root/") {
		t.Fatalf("sessionPath = %s", newRes.SessionPath)
	}
	var read remotehost.FsReadResult
	if err := link.call(ctx, "fs/read", remotehost.FsReadParams{SessionID: "smoke", Path: "hostname"}, &read); err != nil {
		t.Fatalf("fs/read: %v", err)
	}
	if read.Kind != "text" || strings.TrimSpace(read.Text) == "" {
		t.Fatalf("fs/read /etc/hostname = %+v", read)
	}
	var list remotehost.FsListResult
	if err := link.call(ctx, "fs/list", remotehost.FsListParams{SessionID: "smoke", Path: "ssh"}, &list); err != nil || len(list.Entries) == 0 {
		t.Fatalf("fs/list /etc/ssh = %+v err=%v", list, err)
	}
	_ = link.call(ctx, "session/close", remotehost.SessionRef{SessionID: "smoke"}, nil)
}
