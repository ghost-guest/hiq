package nettrans

import (
	"context"
	"net"
	"time"

	"github.com/zzycxz/hiq/internal/trustdomain"
)

// WorkHandler executes an authorized delegation. The embedding service
// registers it (e.g. netdev read-only diagnostics); Serve has already run
// the full §7.3 verification gate before the handler sees anything. The
// handler receives the verified delegation context plus the bound payload.
// Returning an error refuses the work and is reported to the requester.
type WorkHandler func(node *trustdomain.Node, del *trustdomain.Delegation, payload []byte) ([]byte, error)

// Serve accepts inbound peer connections until ctx is cancelled. Every
// connection must pass the membership handshake (the dialer's static key
// is resolved from THIS node's ledger — active members only) before any
// request is answered. Requests are pull-based: status, block ranges and
// approval co-signatures, mirroring the Peer surface. Delegations execute
// through handler when non-nil, otherwise they are refused.
func Serve(ctx context.Context, ln net.Listener, id *trustdomain.Identity, node *trustdomain.Node, handler WorkHandler) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return err
			}
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return nil
		default:
		}
		go handleConn(ctx, conn, id, node, handler)
	}
}

// Listen starts a TCP listener on addr (":0" picks a free port).
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func handleConn(ctx context.Context, conn net.Conn, id *trustdomain.Identity, node *trustdomain.Node, handler WorkHandler) {
	defer func() { _ = conn.Close() }()

	// Membership gate: the ledger is the trust store. A rejected dialer
	// gets the connection closed with no explanation (enumeration guard).
	lookup := ChainLookup(node.Chain())
	sess, err := serveHandshake(conn, id, lookup)
	if err != nil {
		return
	}

	// Re-check after handshake: a member revoked between lookup and now
	// (or while the connection lives) must not keep service. Cheap check
	// per request: this is the revocation-on-sight posture at transport
	// level (spec §6.4).
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !node.Chain().State().IsMember(sess.PeerID) {
			return
		}
		var m msg
		if err := sess.recv(&m); err != nil {
			return
		}
		if err := serveMsg(sess, m, node, handler); err != nil {
			return
		}
	}
}

func serveMsg(sess *session, m msg, node *trustdomain.Node, handler WorkHandler) error {
	switch m.Kind {
	case kindStatus:
		st := trustdomain.StatusOf(node.Chain())
		return sess.send(msg{Kind: kindStatus, Status: &st})

	case kindGetBlocks:
		blocks := node.Chain().Blocks()
		h := node.Chain().Height()
		if m.From > h {
			return sess.send(msg{Kind: kindBlocks})
		}
		to := m.To
		if to > h {
			to = h
		}
		return sess.send(msg{Kind: kindBlocks, Blocks: blocks[m.From : to+1]})

	case kindApprove:
		// The node applies the full policy itself (active admin + quorum
		// type); the ledger re-verifies everything when the block lands.
		ap := node.ApproveRecord(m.Rec, m.Parent)
		if ap == nil {
			return sess.send(msg{Kind: kindErr, Err: "refused"})
		}
		return sess.send(msg{Kind: kindApprove, Approval: ap})

	case kindApproveCk:
		// Admin signs only what it can see in its own chain.
		ap := node.ApproveCheckpoint(m.Ck)
		if ap == nil {
			return sess.send(msg{Kind: kindErr, Err: "refused"})
		}
		return sess.send(msg{Kind: kindApprove, Approval: ap})

	case kindDelegate:
		// The §7.3 gate runs HERE, before any handler code: identity,
		// token, scope, freshness and payload binding are checked against
		// this node's own ledger. Executors only ever see verified work.
		if handler == nil {
			return sess.send(msg{Kind: kindErr, Err: "no executor registered"})
		}
		if err := node.Chain().State().VerifyDelegation(m.Del, m.Payload, nowSec()); err != nil {
			return sess.send(msg{Kind: kindErr, Err: err.Error()})
		}
		out, err := handler(node, m.Del, m.Payload)
		if err != nil {
			return sess.send(msg{Kind: kindErr, Err: err.Error()})
		}
		return sess.send(msg{Kind: kindResult, Out: out})

	default:
		return sess.send(msg{Kind: kindErr, Err: "unknown kind"})
	}
}

func nowSec() uint64 { return uint64(time.Now().Unix()) }
