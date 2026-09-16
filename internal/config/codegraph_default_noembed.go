//go:build !codegraph_embed

package config

// codegraphDefaultEnabled is the default for [codegraph] enabled.
//
// A non-embedded build has no runtime in the binary, so turning the feature on
// would trigger a network download the user never asked for. It keeps the
// upstream opt-in default (off); users enable it in Settings or via
// [codegraph] enabled = true.
func codegraphDefaultEnabled() bool { return false }
