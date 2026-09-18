package mobilebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zzycxz/hiq/internal/linkpeersignal"
)

// extractParam pulls a query param from a "scheme://host?k=v&k2=v2" string
// (url.Parse dislikes our custom "linkpeer://" scheme, so do it by hand).
func extractParam(s, key string) string {
	i := strings.Index(s, key+"=")
	if i < 0 {
		return ""
	}
	s = s[i+len(key)+1:]
	if j := strings.IndexByte(s, '&'); j >= 0 {
		return s[:j]
	}
	return s
}

type collectHandler struct{ out chan SignalMsg }

func (h *collectHandler) OnSignalMsg(m SignalMsg) { h.out <- m }

// TestPairingFullFlowWithRealK drives pair/register → exchange → confirm →
// unpair against a REAL linkpeersignal.Server. This is the K↔S protocol-
// consistency gate: it proves hiq's pairing client and the cloud signal
// agree on every field, status code, and key encoding (LINKPEER_VERIFICATION_PLAN §3 M1).
func TestPairingFullFlowWithRealK(t *testing.T) {
	kSrv := linkpeersignal.NewServer(linkpeersignal.DefaultConfig(), linkpeersignal.NewAudit("error"))
	ts := httptest.NewServer(kSrv.Routes())
	defer ts.Close()

	sPub, sPriv, _ := GenerateLongTerm()
	store := NewMemoryKeyStore()
	// persist S long-term key so a later Bridge round-trips it
	_ = store.Set("mobilebridge.device.priv", sPriv)
	pairing := NewPairing(ts.URL, sPub, store, NewAudit("error"))

	// 1. S registers → gets code + QR URL carrying the out-of-band fingerprint.
	code, qrURL, err := pairing.StartPairing()
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code len %d", len(code))
	}
	pairID := extractParam(qrURL, "pid")
	if pairID == "" {
		t.Fatal("no pid in qrURL")
	}
	// the QR's fp must match S's real fingerprint (out-of-band MITM check)
	if got := extractParam(qrURL, "fp"); got != Fingerprint(sPub) {
		t.Fatalf("qr fp %q != real %q", got, Fingerprint(sPub))
	}

	// 2. C (simulated) exchanges against K. The devId here is derived exactly
	//    as K will check (self-consistency), proving the encoding matches.
	cPub, _, _ := GenerateLongTerm()
	cDev := DevID(cPub)
	exBody, _ := json.Marshal(map[string]string{
		"pairId": pairID, "code": code,
		"devC": cDev, "pubC": b64(cPub), "fpC": Fingerprint(cPub),
	})
	resp, err := http.Post(ts.URL+"/pair/exchange", "application/json", bytes.NewReader(exBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("exchange status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. K pushed pubC to S over WS in production; here we feed it to Pairing
	//    directly (the WS delivery is exercised in TestSignalClientReceivesMsg).
	pairing.OnExchange(SignalMsg{
		Type: "pair_exchange", PairID: pairID,
		DevC: cDev, PubC: b64(cPub), FpC: Fingerprint(cPub),
	})
	if err := pairing.Confirm(pairID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// 4. paired: PeerPub returns C's stored key.
	got, ok := pairing.PeerPub(cDev)
	if !ok || !bytes.Equal(got, cPub) {
		t.Fatal("PeerPub did not return C's stored key")
	}
	if pairing.IsRevoked(cDev) {
		t.Fatal("freshly paired C should not be revoked")
	}

	// 5. wrong code on a fresh pair → 401, locks after 5 (K-side behavior).
	code2, _, _ := pairing.StartPairing()
	badBody, _ := json.Marshal(map[string]string{
		"pairId": extractParam(pairing.lastQR(), "pid"), "code": "WRONG",
		"devC": cDev, "pubC": b64(cPub), "fpC": Fingerprint(cPub),
	})
	_ = code2
	resp2, _ := http.Post(ts.URL+"/pair/exchange", "application/json", bytes.NewReader(badBody))
	if resp2.StatusCode != 401 {
		t.Fatalf("wrong code want 401, got %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	// 6. unpair → revoked.
	pairing.Unpair(cDev)
	if _, ok := pairing.PeerPub(cDev); ok {
		t.Fatal("unpaired C should not be retrievable")
	}
	if !pairing.IsRevoked(cDev) {
		t.Fatal("unpaired C should be revoked")
	}
}

// lastQR is a tiny test-only helper that re-runs StartPairing to mint a fresh
// pairId for the bad-code subtest. (Production never needs this; tests do.)
func (p *Pairing) lastQR() string {
	_, qr, err := p.StartPairing()
	if err != nil {
		return ""
	}
	return qr
}

// TestSignalClientConnectsToRealK proves S's outbound WSS link authenticates
// statelessly and registers in K's peers table — the precondition for C ever
// being able to "find" S (PROTOCOL §4.5).
func TestSignalClientConnectsToRealK(t *testing.T) {
	kSrv := linkpeersignal.NewServer(linkpeersignal.DefaultConfig(), linkpeersignal.NewAudit("error"))
	ts := httptest.NewServer(kSrv.Routes())
	defer ts.Close()

	pub, priv, _ := GenerateLongTerm()
	h := &collectHandler{out: make(chan SignalMsg, 5)}
	sc := NewSignalClient("ws"+ts.URL[len("http"):], pub, priv, h, NewAudit("error"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go sc.Run(ctx)

	// Wait until K sees S online (peers table non-empty). This means the WSS
	// auth (stateless, URL-safe b64) succeeded and S registered itself.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if kSrv.OnlinePeers() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kSrv.OnlinePeers() == 0 {
		t.Fatal("SignalClient never registered on K")
	}
}
