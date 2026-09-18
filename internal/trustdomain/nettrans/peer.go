package nettrans

import (
	"fmt"
	"net"
	"sync"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// NetPeer is a lazily-dialed remote member implementing trustdomain.Peer.
// The connection is established on first use and reused; any transport
// error drops it so the next call redials (a flaky peer must look like a
// temporarily empty one, never a fatal one — Node.Tick tolerates that).
//
// Not safe for concurrent use: the Node loop drives it sequentially, which
// the Peer surface assumes anyway.
type NetPeer struct {
	addr   string
	id     *trustdomain.Identity
	lookup KeyLookup

	mu   sync.Mutex
	sess *session
}

// NewNetPeer prepares a peer for addr ("host:port"); no I/O happens yet.
func NewNetPeer(addr string, id *trustdomain.Identity, lookup KeyLookup) *NetPeer {
	return &NetPeer{addr: addr, id: id, lookup: lookup}
}

// ensure returns the live session, dialing if needed.
func (p *NetPeer) ensure() (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil {
		return p.sess, nil
	}
	conn, err := net.DialTimeout("tcp", p.addr, hsTimeout)
	if err != nil {
		return nil, err
	}
	sess, err := dialHandshake(conn, p.id, p.lookup)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	p.sess = sess
	return sess, nil
}

// drop forgets a broken session.
func (p *NetPeer) drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil {
		p.sess.close()
		p.sess = nil
	}
}

// roundtrip sends m and reads the reply message.
func (p *NetPeer) roundtrip(m msg) (msg, error) {
	s, err := p.ensure()
	if err != nil {
		return msg{}, err
	}
	if err := s.send(m); err != nil {
		p.drop()
		return msg{}, err
	}
	var reply msg
	if err := s.recv(&reply); err != nil {
		p.drop()
		return msg{}, err
	}
	if reply.Kind == kindErr {
		return reply, errRejected
	}
	return reply, nil
}

// PeerID returns the remote member's trustdomain ID (dials on first use).
func (p *NetPeer) PeerID() string {
	s, err := p.ensure()
	if err != nil {
		return p.addr // unresolved: never collides with a member ID
	}
	return s.PeerID
}

// Status fetches the peer's chain status.
func (p *NetPeer) Status() trustdomain.Status {
	reply, err := p.roundtrip(msg{Kind: kindStatus})
	if err != nil || reply.Status == nil {
		return trustdomain.Status{}
	}
	return *reply.Status
}

// Blocks pulls the inclusive [from, to] range.
func (p *NetPeer) Blocks(from, to uint64) []*trustdomain.Block {
	reply, err := p.roundtrip(msg{Kind: kindGetBlocks, From: from, To: to})
	if err != nil {
		return nil
	}
	return reply.Blocks
}

// Approve asks the remote admin to co-sign a quorum record.
func (p *NetPeer) Approve(rec *trustdomain.Record, parent trustdomain.Hash) *trustdomain.Approval {
	reply, err := p.roundtrip(msg{Kind: kindApprove, Rec: rec, Parent: parent})
	if err != nil || reply.Approval == nil {
		return nil
	}
	return reply.Approval
}

// ApproveCkpt asks the remote admin to co-sign a checkpoint.
func (p *NetPeer) ApproveCkpt(ck *trustdomain.Checkpoint) *trustdomain.Approval {
	reply, err := p.roundtrip(msg{Kind: kindApproveCk, Ck: ck})
	if err != nil || reply.Approval == nil {
		return nil
	}
	return reply.Approval
}

// Delegate sends a signed work request (spec §7.3) and returns the
// executor's output. A refused or failed execution surfaces as an error.
func (p *NetPeer) Delegate(d *trustdomain.Delegation, payload []byte) ([]byte, error) {
	reply, err := p.roundtrip(msg{Kind: kindDelegate, Del: d, Payload: payload})
	if err != nil {
		return nil, err
	}
	if reply.Kind == kindErr {
		return nil, fmt.Errorf("nettrans: executor refused: %s", reply.Err)
	}
	if reply.Kind != kindResult {
		return nil, fmt.Errorf("nettrans: unexpected reply %q", reply.Kind)
	}
	return reply.Out, nil
}
