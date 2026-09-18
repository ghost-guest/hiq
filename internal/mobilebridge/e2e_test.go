package mobilebridge

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/zzycxz/hiq/internal/linkpeersignal"
	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
)

// mockExecutor records Submit inputs for assertion.
type mockExecutor struct {
	mu      sync.Mutex
	submits []string
}

func (m *mockExecutor) Submit(_, input, _ string) error {
	m.mu.Lock()
	m.submits = append(m.submits, input)
	m.mu.Unlock()
	return nil
}
func (m *mockExecutor) Cancel(string) error                               { return nil }
func (m *mockExecutor) Steer(string, string) error                        { return nil }
func (m *mockExecutor) Pause(string) error                                { return nil }
func (m *mockExecutor) Resume(string) error                               { return nil }
func (m *mockExecutor) Approve(string, string, bool, bool, bool) error    { return nil }
func (m *mockExecutor) Answer(string, string, []string) error             { return nil }
func (m *mockExecutor) SetPlan(string, bool) error                        { return nil }
func (m *mockExecutor) SetModel(string, string) error                     { return nil }
func (m *mockExecutor) ListSessions() ([]SessionInfo, error)              { return nil, nil }
func (m *mockExecutor) ListModels() ([]ModelInfo, error) { return nil, nil }
func (m *mockExecutor) ListTemplates() ([]TemplateInfo, error) { return nil, nil }
func (m *mockExecutor) NewTab(_, _ string) (string, error)                { return "", nil }
func (m *mockExecutor) RenameSession(string, string) error                { return nil }
func (m *mockExecutor) DeleteSession(string) error                        { return nil }
func (m *mockExecutor) OfficeRun(string, string, map[string]string) error { return nil }
func (m *mockExecutor) FileStart(string, string, int64) error             { return nil }
func (m *mockExecutor) FileChunk(string, int, string) error               { return nil }
func (m *mockExecutor) FileEnd(string, string) error                      { return nil }
func (m *mockExecutor) LoadSession(string) ([]map[string]any, error)      { return nil, nil }
func (m *mockExecutor) submitted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.submits...)
}

func dialKWS(t *testing.T, httpURL string, pub ed25519.PublicKey, priv ed25519.PrivateKey) *websocket.Conn {
	t.Helper()
	dev := DevID(pub)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(priv, []byte(dev+ts))
	uEnc := base64.URLEncoding.WithPadding(base64.NoPadding)
	u := "ws" + httpURL[len("http"):] + "/session/ws?dev=" + dev +
		"&ts=" + ts + "&pub=" + uEnc.EncodeToString(pub) + "&sig=" + uEnc.EncodeToString(sig)
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial K: %v", err)
	}
	return c
}

// TestE2EFullLink is the M1 gate (LINKPEER_VERIFICATION_PLAN §3): a real
// linkpeersignal K, a real Bridge S, and a synthetic linkpeer C in-process.
// Drives pair → C-connects-K → offer/answer/ICE over K → DataChannel →
// handshake → AEAD-sealed submit → S's executor records it. Proves the entire
// P2P+encryption stack works Go-internally before Flutter exists.
func TestE2EFullLink(t *testing.T) {
	kSrv := linkpeersignal.NewServer(linkpeersignal.DefaultConfig(), linkpeersignal.NewAudit("error"))
	ts := httptest.NewServer(kSrv.Routes())
	defer ts.Close()

	// S (Bridge)
	sPub, sPriv, _ := GenerateLongTerm()
	store := NewMemoryKeyStore()
	exec := &mockExecutor{}
	cfg := DefaultConfig()
	cfg.SignalURL = ts.URL
	bridge := NewBridge(cfg, sPriv, sPub, store, exec, NewAudit("error"))
	sDev := DevID(sPub)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go bridge.Start(ctx)
	// wait for S online on K
	for i := 0; i < 250 && kSrv.OnlinePeers() == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if kSrv.OnlinePeers() == 0 {
		t.Fatal("S never registered on K")
	}

	// pair
	code, qrURL, err := bridge.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	pairID := extractParam(qrURL, "pid")
	cPub, cPriv, _ := GenerateLongTerm()
	cDev := DevID(cPub)
	exBody, _ := json.Marshal(map[string]string{
		"pairId": pairID, "code": code, "devC": cDev,
		"pubC": b64(cPub), "fpC": Fingerprint(cPub),
	})
	resp, _ := http.Post(ts.URL+"/pair/exchange", "application/json", bytes.NewReader(exBody))
	resp.Body.Close()
	bridge.pairing.OnExchange(SignalMsg{
		Type: "pair_exchange", PairID: pairID,
		DevC: cDev, PubC: b64(cPub), FpC: Fingerprint(cPub),
	})
	if err := bridge.ConfirmPairing(pairID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// C side
	cWS := dialKWS(t, ts.URL, cPub, cPriv)
	defer cWS.Close()
	cPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer cPC.Close()
	dc, err := cPC.CreateDataChannel("linkpeer", nil)
	if err != nil {
		t.Fatal(err)
	}

	cTs := time.Now().UnixMilli()
	nc := bytes.Repeat([]byte{1}, 16)
	cEph, _ := GenerateEphemeral()
	cEphPub := cEph.PublicKey().Bytes()

	var (
		mu            sync.Mutex
		cState        = "wait_sh"
		chJSONSent    []byte // exact bytes C sent as ClientHello (for transcript)
		cC2S, cS2C    cipher.AEAD
		cSendSeq      uint64
		cRecvMax      uint64
		cRecvDone     bool
		encryptedChan = make(chan struct{})
	)

	cSeal := func(pt []byte) []byte {
		mu.Lock()
		seq := cSendSeq
		cSendSeq++
		mu.Unlock()
		nonce, _ := Random(12)
		return SealFrame(cC2S, seq, nonce, pt)
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		mu.Lock()
		st := cState
		mu.Unlock()
		if st == "wait_sh" {
			var sh proto.ServerHello
			if json.Unmarshal(msg.Data, &sh) != nil || sh.T != "hello_s" {
				return
			}
			if err := VerifyServerHello(sPub, sh); err != nil {
				t.Errorf("verify sh: %v", err)
				return
			}
			sEph, _ := b64d(sh.Eph)
			ns, _ := b64d(sh.Ns)
			hs, err := CompleteHandshake(cEph, sEph, nc, ns, chJSONSent, msg.Data)
			if err != nil {
				t.Errorf("C derive: %v", err)
				return
			}
			cC2S, _ = NewAEAD(hs.C2S)
			cS2C, _ = NewAEAD(hs.S2C)
			finJSON, _ := json.Marshal(FinishedMessage("c", hs.Transcript))
			_ = dc.Send(cSeal(finJSON))
			mu.Lock()
			cState = "wait_fin"
			mu.Unlock()
			return
		}
		if st == "wait_fin" {
			seq, pt, err := OpenFrame(cS2C, msg.Data)
			if err != nil {
				return
			}
			mu.Lock()
			if cRecvDone && seq <= cRecvMax {
				mu.Unlock()
				return
			}
			cRecvMax = seq
			cRecvDone = true
			mu.Unlock()
			var fin proto.Finished
			json.Unmarshal(pt, &fin)
			if fin.T == "fin" && fin.Role == "s" {
				mu.Lock()
				cState = "encrypted"
				mu.Unlock()
				close(encryptedChan)
			}
		}
	})

	dc.OnOpen(func() {
		ch := BuildClientHello(cPriv, cEphPub, nc, cDev, sDev, cTs)
		b, _ := json.Marshal(ch)
		mu.Lock()
		chJSONSent = b
		mu.Unlock()
		_ = dc.Send(b)
	})

	// cWS 写互斥：offer（主流程）与 ICE 候选回调（pion 的 gather goroutine）
	// 并发写同一连接会让 gorilla/websocket panic（concurrent write）。
	var wsMu sync.Mutex
	cPC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		b, _ := json.Marshal(SignalMsg{Type: "ice", From: cDev, To: sDev, ConnID: "e2e", Cand: c.ToJSON().Candidate})
		wsMu.Lock()
		defer wsMu.Unlock()
		_ = cWS.WriteMessage(websocket.TextMessage, b)
	})

	// K→C pump: deliver answer + ice to C's PC
	go func() {
		for {
			_, raw, err := cWS.ReadMessage()
			if err != nil {
				return
			}
			var m SignalMsg
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			switch m.Type {
			case "answer":
				cPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
			case "ice":
				cPC.AddICECandidate(webrtc.ICECandidateInit{Candidate: m.Cand})
			}
		}
	}()

	offer, err := cPC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cPC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	ob, _ := json.Marshal(SignalMsg{Type: "offer", From: cDev, To: sDev, ConnID: "e2e", SDP: offer.SDP})
	wsMu.Lock()
	err = cWS.WriteMessage(websocket.TextMessage, ob)
	wsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-encryptedChan:
	case <-time.After(15 * time.Second):
		t.Fatal("handshake did not complete in 15s")
	}

	// encrypted submit
	submit, _ := json.Marshal(map[string]string{"t": "submit", "tab": "tab1", "input": "hello from C"})
	_ = dc.Send(cSeal(submit))

	// assert S received
	for i := 0; i < 150; i++ {
		if len(exec.submitted()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := exec.submitted()
	if len(got) == 0 || got[0] != "hello from C" {
		t.Fatalf("S did not receive submit; got %v", got)
	}
}
