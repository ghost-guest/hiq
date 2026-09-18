// Package mobilebridge bridges hiq's desktop Controller to linkpeer
// mobile clients over an end-to-end-encrypted WebRTC P2P link.
//
// M0-spike status: this package currently only carries the pion/webrtc
// dependency and a smoke test (TestPionEcho) proving pure-Go WebRTC works
// inside hiq's CGO_ENABLED=0 build. The bridge itself is implemented
// in M1. See docs/LINKPEER_HIQ_SPEC.md.
package mobilebridge
