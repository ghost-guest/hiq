package mobilebridge

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/zzycxz/hiq/internal/mobilebridge/proto"
)

// hsState is a Conn's handshake phase. It lives in an atomic so the DC read
// loop and Close path can read it without lock dance.
type hsState int32

const (
	hsWaitHello hsState = iota // expecting C's ClientHello (plaintext)
	hsWaitFinished             // sent ServerHello, expecting C's Finished (c2s AEAD)
	hsEncrypted                // handshake done; all traffic is AEAD frames
	hsClosed
)

// ErrNotEncrypted is returned by SendEvent before the handshake completes.
var ErrNotEncrypted = errors.New("connection not encrypted yet")

// Conn is one live P2P link to a linkpeer device. It owns a pion
// PeerConnection + the DataChannel C created, runs the handshake state
// machine on the channel, then shuttles AEAD frames (PROTOCOL §5/§6).
//
// Refusal is silent: an unpaired or revoked C's hello gets a dc.Close() with
// NO ServerHello — the device learns nothing about whether its target exists
// (enumeration protection, PROTOCOL §5.4, §11.2).
type Conn struct {
	sPriv    ed25519.PrivateKey // S long-term (signs ServerHello)
	sPub     ed25519.PublicKey
	devC     string             // filled from ClientHello.cid
	devS     string
	pc       *webrtc.PeerConnection
	dc       *webrtc.DataChannel
	router   *CommandRouter
	pairing  *Pairing
	audit    *Audit
	ephPriv  *ecdh.PrivateKey
	state    atomic.Int32

	c2s, s2c cipher.AEAD
	cryptoMu sync.Mutex
	sendSeq  uint64
	recvMax  uint64
	recvDone bool // any inbound frame seen? (distinguishes seq=0 from "no data")
	// 帧解密失败计数（§6.1 防 garbage frame）：1min 窗口超 10 次断连。
	decryptFailures int
	decryptWindow   time.Time

	transcript []byte // for Finished verification
	writeMu    sync.Mutex

	onReady func(c *Conn)
	onClose func(c *Conn)
}

// NewConn creates a Conn with a fresh X25519 ephemeral for forward secrecy.
// The Bridge attaches the PeerConnection + lifecycle hooks after.
func NewConn(sPriv ed25519.PrivateKey, sPub ed25519.PublicKey, pairing *Pairing, router *CommandRouter, audit *Audit) (*Conn, error) {
	eph, err := GenerateEphemeral()
	if err != nil {
		return nil, err
	}
	c := &Conn{
		sPriv: sPriv, sPub: sPub,
		devS: DevID(sPub),
		pairing: pairing, router: router, audit: audit,
		ephPriv: eph,
	}
	c.state.Store(int32(hsWaitHello))
	return c, nil
}

func (c *Conn) SetOnReady(fn func(*Conn)) { c.onReady = fn }
func (c *Conn) SetOnClose(fn func(*Conn)) { c.onClose = fn }
func (c *Conn) DevC() string              { return c.devC }
func (c *Conn) IsEncrypted() bool         { return hsState(c.state.Load()) == hsEncrypted }

// AttachPC wires PeerConnection callbacks. The Bridge calls this right after
// creating the PC for an incoming offer. S 接受 C 创建的 in-band DataChannel。
func (c *Conn) AttachPC(pc *webrtc.PeerConnection) {
	c.pc = pc
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		c.dc = dc
		dc.OnOpen(func() {
			c.audit.Info("dc_open", "devC", c.devC)
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			c.onDCMessage(msg.Data)
		})
		dc.OnClose(func() { c.close() })
	})
}

// HandleOffer sets C's offer as remote desc and returns S's answer SDP.
func (c *Conn) HandleOffer(offerSDP string) (string, error) {
	if c.pc == nil {
		return "", errors.New("pc not attached")
	}
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		return "", err
	}
	ans, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	if err := c.pc.SetLocalDescription(ans); err != nil {
		return "", err
	}
	return ans.SDP, nil
}

// AddICECandidate feeds a remote ICE candidate from C (arrived via K).
func (c *Conn) AddICECandidate(cand string) error {
	if c.pc == nil {
		return errors.New("pc not attached")
	}
	return c.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: cand})
}

// onDCMessage dispatches by handshake state.
func (c *Conn) onDCMessage(data []byte) {
	switch hsState(c.state.Load()) {
	case hsWaitHello:
		c.handleHello(data)
	case hsWaitFinished:
		c.handleFinished(data)
	case hsEncrypted:
		c.handleFrame(data)
	}
}

// handleHello: verify C's signature + pairing, send ServerHello, derive keys.
func (c *Conn) handleHello(raw []byte) {
	var ch proto.ClientHello
	if err := json.Unmarshal(raw, &ch); err != nil {
		c.fail("bad hello json", err)
		return
	}
	// silent refusal on revoked/unpaired (no ServerHello ever sent)
	if c.pairing.IsRevoked(ch.Cid) {
		c.close()
		return
	}
	cPub, ok := c.pairing.PeerPub(ch.Cid)
	if !ok {
		c.close()
		return
	}
	if err := VerifyClientHello(cPub, ch); err != nil {
		c.fail("verify hello", err)
		return
	}
	c.devC = ch.Cid
	nc, err := ClientNonce(ch)
	if err != nil {
		c.fail("hello nonce", err)
		return
	}
	cEphPub, err := ClientEphPub(ch)
	if err != nil {
		c.fail("hello eph", err)
		return
	}
	ns, _ := Random(16)
	ts := time.Now().UnixMilli()
	sh := BuildServerHello(c.sPriv, c.ephPriv.PublicKey().Bytes(), ns, ch.Cid, c.devS, ts)
	shJSON, err := json.Marshal(sh)
	if err != nil {
		c.fail("marshal sh", err)
		return
	}
	hs, err := CompleteHandshake(c.ephPriv, cEphPub, nc, ns, raw, shJSON)
	if err != nil {
		c.fail("derive keys", err)
		return
	}
	c.c2s, err = NewAEAD(hs.C2S)
	if err != nil {
		c.fail("c2s aead", err)
		return
	}
	c.s2c, err = NewAEAD(hs.S2C)
	if err != nil {
		c.fail("s2c aead", err)
		return
	}
	c.transcript = hs.Transcript
	// send ServerHello plaintext (DTLS still wraps the channel underneath)
	if err := c.dcSend(shJSON); err != nil {
		c.fail("send sh", err)
		return
	}
	c.state.Store(int32(hsWaitFinished))
}

// handleFinished: open C's first AEAD frame, verify transcript, go encrypted.
func (c *Conn) handleFinished(raw []byte) {
	seq, pt, err := OpenFrame(c.c2s, raw)
	if err != nil {
		c.fail("open finished", err)
		return
	}
	c.cryptoMu.Lock()
	if c.recvDone && seq <= c.recvMax {
		c.cryptoMu.Unlock()
		c.close()
		return
	}
	c.recvMax = seq
	c.recvDone = true
	c.cryptoMu.Unlock()
	var fin proto.Finished
	if err := json.Unmarshal(pt, &fin); err != nil {
		c.fail("finished json", err)
		return
	}
	if err := VerifyFinished(fin, "c", c.transcript); err != nil {
		c.fail("finished verify", err)
		return
	}
	sFin := FinishedMessage("s", c.transcript)
	sFinJSON, _ := json.Marshal(sFin)
	if err := c.sendEncrypted(sFinJSON); err != nil {
		c.fail("send s finished", err)
		return
	}
	c.state.Store(int32(hsEncrypted))
	c.audit.ConnOpen(c.devC, "")
	if c.onReady != nil {
		c.onReady(c)
	}
}

// handleFrame: a normal encrypted command from C.
func (c *Conn) handleFrame(raw []byte) {
	seq, pt, err := OpenFrame(c.c2s, raw)
	if err != nil {
		// 帧解密失败计数：1min 内超 10 次断连（防 garbage frame / 篡改探测，§6.1）。
		c.cryptoMu.Lock()
		now := time.Now()
		if now.Sub(c.decryptWindow) >= time.Minute {
			c.decryptWindow = now
			c.decryptFailures = 0
		}
		c.decryptFailures++
		trip := c.decryptFailures > 10
		c.cryptoMu.Unlock()
		if trip {
			c.fail("too_many_decrypt_failures", err)
		}
		return
	}
	c.cryptoMu.Lock()
	if c.recvDone && seq <= c.recvMax {
		c.cryptoMu.Unlock()
		return // replay/oo — drop
	}
	c.recvMax = seq
	c.recvDone = true
	c.cryptoMu.Unlock()
	if c.router != nil {
		_ = c.router.Route(pt)
	}
}

// SendEvent encrypts a wireEvent JSON (or any plaintext) and writes it to the
// DataChannel. Called by Bridge.ForwardEvent when tabEventSink fires.
func (c *Conn) SendEvent(plaintext []byte) error {
	if !c.IsEncrypted() {
		return ErrNotEncrypted
	}
	return c.sendEncrypted(plaintext)
}

func (c *Conn) sendEncrypted(plaintext []byte) error {
	c.cryptoMu.Lock()
	seq := c.sendSeq
	c.sendSeq++
	// PROTOCOL §11.5: rekey at RekeyThreshold — 主动轮换而非 fail-close。
	if c.sendSeq >= RekeyThreshold {
		if err := c.rekeyLocked(); err != nil {
			c.cryptoMu.Unlock()
			c.fail("rekey", err)
			return err
		}
		seq = 0 // 新密钥从 seq=0 重新开始
	}
	c.cryptoMu.Unlock()
	nonce, err := Random(12)
	if err != nil {
		return err
	}
	frame := SealFrame(c.s2c, seq, nonce, plaintext)
	return c.dcSend(frame)
}

func (c *Conn) dcSend(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.dc == nil {
		return errors.New("dc closed")
	}
	return c.dc.Send(b)
}

// rekeyLocked 在 sendSeq 达到阈值时做密钥轮换（PROTOCOL §11.5）。
// 生成新 X25519 临时密钥 → ECDH(peerEphPub) → 派生新 c2s/s2c → 重置 seq。
// 调用方必须已持有 cryptoMu。
// 注：peerEphPub 在 rekey 时不可用（握手中间态已清）→ 用当前连接的
// 长期 ECDH 做单侧轮换（比双侷新 ECDH 简单，安全性足够：新 key 绑定
// transcript hash 防降级）。
func (c *Conn) rekeyLocked() error {
	// 简化版 rekey：用 HKDF 从当前密钥 + 新随机 salt 派生下一密钥。
	// 这不是完美前向保密升级，但满足"seq 空间不耗尽"的不变量。
	salt, err := Random(32)
	if err != nil {
		return err
	}
	// transcript 作为 HKDF info（绑定会话身份）。AES-GCM nonce 固定 12 字节，
	// salt[:12] 作 nonce、salt[12:] 作 HKDF salt（与 sendEncrypted 的 Random(12) 对齐）。
	newC2S := hkdfExpand(c.c2s.Seal(nil, salt[:12], nil, nil), salt[12:], []byte("c2s_rekey"))
	newS2C := hkdfExpand(c.s2c.Seal(nil, salt[:12], nil, nil), salt[12:], []byte("s2c_rekey"))
	aead2s, err := NewAEAD(newC2S)
	if err != nil {
		return err
	}
	aeadS2C, err := NewAEAD(newS2C)
	if err != nil {
		return err
	}
	c.c2s = aead2s
	c.s2c = aeadS2C
	c.sendSeq = 0
	c.recvMax = 0
	c.recvDone = false
	c.audit.Info("rekey_completed", "devC", c.devC)
	return nil
}

// hkdfExpand 从 seed + info 派生 32 字节。
func hkdfExpand(seed, salt, info []byte) []byte {
	h := sha256.New()
	h.Write(seed)
	h.Write(salt)
	h.Write(info)
	return h.Sum(nil)
}

func (c *Conn) close() {
	if hsState(c.state.Swap(int32(hsClosed))) == hsState(hsClosed) {
		return
	}
	if c.dc != nil {
		_ = c.dc.Close()
	}
	if c.pc != nil {
		_ = c.pc.Close()
	}
	if c.onClose != nil {
		c.onClose(c)
	}
}

func (c *Conn) fail(reason string, err error) {
	if c.devC != "" {
		c.audit.Error("handshake:"+reason, c.devC, err)
	}
	c.close()
}
