package main

import (
	"testing"

	"github.com/zzycxz/hiq/internal/config"
)

// Global (empty-root) sessions must resolve to ONE fixed, profile-partitioned
// directory — never the process CWD (regression: dev globals used to scatter
// into CWD-derived project dirs).
func TestGlobalSessionDirFixedPerProfile(t *testing.T) {
	cases := []struct{ profile, want string }{
		{"", config.SessionDir()},
		{"dev", config.SessionDir()},
		{"default", config.SessionDir()},
		{"cowork", config.SessionDirFor("cowork")},
		{"netdev", config.SessionDirFor("netdev")},
	}
	for _, c := range cases {
		if got := desktopSessionDirFor("", c.profile); got != c.want {
			t.Fatalf("desktopSessionDirFor(\"\", %q) = %q, want %q", c.profile, got, c.want)
		}
	}
	if got := desktopSessionDir(""); got != config.SessionDir() {
		t.Fatalf("desktopSessionDir(\"\") = %q, want %q (CWD independence)", got, config.SessionDir())
	}
}
