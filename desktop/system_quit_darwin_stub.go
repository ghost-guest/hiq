//go:build darwin && !cgo

package main

// Stub implementations for darwin/amd64 builds where cgo is not available.
// They provide no‑op functions so the linker can resolve the symbols.

func installSystemQuitHook() {
	// No operation on amd64 macOS builds.
}

//export hiqMarkSystemQuit
func hiqMarkSystemQuit() {
	// No operation on amd64 macOS builds.
}
