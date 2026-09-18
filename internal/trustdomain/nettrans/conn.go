package nettrans

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/zzycxz/hiq/internal/mobilebridge"
	"github.com/zzycxz/hiq/internal/trustdomain"
)

// msgIO timeout for individual request/response exchanges. Generous for
// large block range pulls over slow links.
const ioTimeout = 60 * time.Second

// minFrameLen mirrors mobilebridge's private frameMinLen (ver 1 + seq 8 +
// nonce 12 + GCM tag 16). Anything shorter cannot be a valid frame.
const minFrameLen = 1 + 8 + 12 + 16

// send marshals v, seals it in an AEAD frame, and writes it with the
// length prefix. Frames are strictly sequential per direction; the mutex
// serializes writers.
func (s *session) send(v any) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	<-s.sendMu
	defer func() { s.sendMu <- struct{}{} }()

	nonce, err := mobilebridge.Random(12)
	if err != nil {
		return err
	}
	frame := mobilebridge.SealFrame(s.sendAE, s.sendSN, nonce, plain)
	s.sendSN++
	var len4 [4]byte
	binary.BigEndian.PutUint32(len4[:], uint32(len(frame)))
	if _, err := s.conn.Write(len4[:]); err != nil {
		return err
	}
	_, err = s.conn.Write(frame)
	return err
}

// recv reads one frame, authenticates it, enforces in-order sequence
// numbers (anti-replay), and unmarshals into v.
func (s *session) recv(v any) error {
	_ = s.conn.SetReadDeadline(time.Now().Add(ioTimeout))
	var len4 [4]byte
	if _, err := io.ReadFull(s.conn, len4[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(len4[:])
	if n < minFrameLen || n > maxRawLen {
		return fmt.Errorf("nettrans: bad frame length %d", n)
	}
	frame := make([]byte, n)
	if _, err := io.ReadFull(s.conn, frame); err != nil {
		return err
	}
	seq, plain, err := mobilebridge.OpenFrame(s.recvAE, frame)
	if err != nil {
		return err
	}
	if seq != s.recvSN {
		return fmt.Errorf("nettrans: frame out of order (%d, want %d)", seq, s.recvSN)
	}
	s.recvSN = seq + 1
	return json.Unmarshal(plain, v)
}

// close shuts the underlying connection.
func (s *session) close() { _ = s.conn.Close() }

// --- wire messages -------------------------------------------------------------

const (
	kindStatus    = "status"
	kindGetBlocks = "getblocks"
	kindBlocks    = "blocks"
	kindApprove   = "approve"
	kindApproveCk = "approve_ckpt"
	kindDelegate  = "delegate"
	kindResult    = "result"
	kindErr       = "error"
)

// msg is the single envelope for all peer exchanges (spec Peer surface).
type msg struct {
	Kind string `json:"kind"`

	// status / getblocks / blocks
	Status *trustdomain.Status  `json:"status,omitempty"`
	From   uint64               `json:"from,omitempty"`
	To     uint64               `json:"to,omitempty"`
	Blocks []*trustdomain.Block `json:"blocks,omitempty"`

	// approve (record co-signature)
	Rec    *trustdomain.Record `json:"rec,omitempty"`
	Parent trustdomain.Hash    `json:"parent,omitempty"`

	// approve_ckpt
	Ck *trustdomain.Checkpoint `json:"ck,omitempty"`

	// delegate (work request + payload) / result (output)
	Del     *trustdomain.Delegation `json:"del,omitempty"`
	Payload []byte                  `json:"payload,omitempty"`
	Out     []byte                  `json:"out,omitempty"`

	// replies
	Approval *trustdomain.Approval `json:"approval,omitempty"`
	Err      string                `json:"err,omitempty"`
}
