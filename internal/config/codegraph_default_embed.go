//go:build codegraph_embed

package config

// codegraphDefaultEnabled is the default for [codegraph] enabled.
//
// An embedded build carries the CodeGraph runtime inside the binary
// (internal/codegraph/assets/codegraph_runtime.bin, packed by the release/portable
// pipeline), so the feature is usable with zero network and zero setup. It
// therefore defaults ON: a copied exe dropped into a fresh folder — where no
// config.toml exists yet to opt in — still comes up with code intelligence.
// An explicit `enabled = false` in the user's config still wins.
func codegraphDefaultEnabled() bool { return true }
