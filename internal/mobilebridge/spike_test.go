package mobilebridge

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// TestPionEcho is the M0-spike gate (LINKPEER_VERIFICATION_PLAN §3): two
// PeerConnections in ONE process connect via host candidates and echo a
// DataChannel message. Proves pion/webrtc v4 works pure-Go inside hiq's
// CGO_ENABLED=0 build — the single static binary invariant must hold.
//
// Pass criteria:
//   - DataChannel established (host candidate, no network, no STUN)
//   - offerer→answerer→offerer round trip completes within timeout
//
// This is Go-only — no Flutter, no real network, no signaling server. It's
// the earliest possible proof the P2P/encryption stack CAN work: this same
// DataChannel is what M1 will layer AEAD frames over (PROTOCOL §6).
func TestPionEcho(t *testing.T) {
	cfg := webrtc.Configuration{ICEServers: []webrtc.ICEServer{}}

	offerer, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		t.Fatalf("offerer PC: %v", err)
	}
	defer offerer.Close()
	answerer, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		t.Fatalf("answerer PC: %v", err)
	}
	defer answerer.Close()

	// Wire ICE candidate exchange directly (in-process signaling).
	offerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = answerer.AddICECandidate(c.ToJSON())
		}
	})
	answerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = offerer.AddICECandidate(c.ToJSON())
		}
	})

	// offerer creates a DataChannel and sends a message on open.
	dc, err := offerer.CreateDataChannel("echo", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	done := make(chan string, 2)
	dc.OnOpen(func() {
		if err := dc.SendText("ping"); err != nil {
			t.Errorf("sendText: %v", err)
		}
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		done <- "offerer:" + string(msg.Data)
	})

	// answerer receives the DataChannel and echoes back.
	answerer.OnDataChannel(func(c *webrtc.DataChannel) {
		c.OnMessage(func(msg webrtc.DataChannelMessage) {
			done <- "answerer:" + string(msg.Data)
			_ = c.SendText("pong")
		})
	})

	// SDP exchange: offer → answer.
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := offerer.SetLocalDescription(offer); err != nil {
		t.Fatalf("offerer SetLocal: %v", err)
	}
	if err := answerer.SetRemoteDescription(offer); err != nil {
		t.Fatalf("answerer SetRemote: %v", err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatalf("answerer SetLocal: %v", err)
	}
	if err := offerer.SetRemoteDescription(answer); err != nil {
		t.Fatalf("offerer SetRemote: %v", err)
	}

	// Wait for both halves of the echo, with a hard timeout.
	deadline := time.After(10 * time.Second)
	var got []string
	for len(got) < 2 {
		select {
		case msg := <-done:
			got = append(got, msg)
		case <-deadline:
			t.Fatalf("echo timed out; got %v", got)
		}
	}
	if got[0] != "answerer:ping" {
		t.Errorf("first = %q, want answerer:ping", got[0])
	}
	if got[1] != "offerer:pong" {
		t.Errorf("second = %q, want offerer:pong", got[1])
	}
}
