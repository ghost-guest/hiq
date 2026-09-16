// Command fairpeer is a config- and plugin-driven coding agent CLI.
package main

import (
	"os"

	"github.com/zzycxz/fairpeer/internal/cli"

	// Blank imports wire compile-time built-ins into their registries.
	_ "github.com/zzycxz/fairpeer/internal/provider/anthropic"
	_ "github.com/zzycxz/fairpeer/internal/provider/openai"
	_ "github.com/zzycxz/fairpeer/internal/provider/responses"
	_ "github.com/zzycxz/fairpeer/internal/tool/builtin"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
