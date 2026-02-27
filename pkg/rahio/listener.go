package rahio

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hossein/rahio/pkg/rahio/scheduler"
)

// pendingConnTimeout is how long the listener waits for all NumSubflows to
// arrive before discarding a partially-assembled connection.
const pendingConnTimeout = 30 * time.Second

// Listener accepts incoming MultipathConn connections.
// It wraps a single TCP listener; all subflows of every client connection
// arrive on the same port and are grouped by ConnectionID (§4.1).
type Listener struct {
	tcpListener net.Listener
	sched       scheduler.SchedulerOps
	mu          sync.Mutex
	pending     map[[16]byte]*pendingConn // partial connections, keyed by ConnectionID
	acceptCh    chan *MultipathConn
	closed      chan struct{}
	closeOnce   sync.Once
}

// pendingConn collects subflows for one ConnectionID until all NumSubflows arrive.
type pendingConn struct {
	subflows    []*Subflow // slot per SubflowIndex
	numExpected int
	arrived     int
	timer       *time.Timer // cleanup timer if group never completes
}

// Listen starts a Rahio listener on addr (e.g. ":9000").
//
// sched is shared across all accepted MultipathConns. Because every
// well-formed scheduler (including RoundRobin) stores state per ConnectionID
// (§7.2), sharing one instance is correct. Pass nil to use round-robin.
func Listen(addr string, sched scheduler.SchedulerOps) (*Listener, error) {
	if sched == nil {
		sched = scheduler.NewRoundRobin()
		slog.Debug("listen: using default round-robin scheduler")
	}

	tcpL, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rahio: listening on %q: %w", addr, err)
	}

	slog.Info("listen: started", "addr", tcpL.Addr())

	l := &Listener{
		tcpListener: tcpL,
		sched:       sched,
		pending:     make(map[[16]byte]*pendingConn),
		acceptCh:    make(chan *MultipathConn, 16),
		closed:      make(chan struct{}),
	}
	go l.acceptLoop()

	return l, nil
}

// Accept blocks until a fully-assembled MultipathConn is ready (all declared
// subflows have completed the handshake) or the listener is closed.
func (l *Listener) Accept() (*MultipathConn, error) {
	select {
	case <-l.closed:
		return nil, fmt.Errorf("rahio: listener closed")
	case conn, ok := <-l.acceptCh:
		if !ok {
			return nil, fmt.Errorf("rahio: listener closed")
		}
		slog.Info("listen: accepted MultipathConn",
			"connID", connIDStr(conn.ConnectionID),
			"numSubflows", len(conn.Subflows),
		)
		return conn, nil
	}
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.tcpListener.Addr()
}

// Close shuts down the listener. Already-accepted MultipathConns are not affected.
func (l *Listener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		slog.Info("listen: closing listener", "addr", l.tcpListener.Addr())
		close(l.closed)
		err = l.tcpListener.Close()
	})
	return err
}

// acceptLoop accepts raw TCP connections and hands each to a handleSubflow goroutine.
func (l *Listener) acceptLoop() {
	slog.Debug("listen: acceptLoop started", "addr", l.tcpListener.Addr())
	for {
		tcpConn, err := l.tcpListener.Accept()
		if err != nil {
			select {
			case <-l.closed:
				slog.Debug("listen: acceptLoop stopping, listener closed")
				return
			default:
				slog.Warn("listen: accept error (transient)", "err", err)
				continue
			}
		}
		slog.Debug("listen: accepted raw TCP connection", "remote", tcpConn.RemoteAddr())
		go l.handleSubflow(tcpConn)
	}
}

// handleSubflow reads the HANDSHAKE packet from one TCP connection, sends
// HANDSHAKE_ACK immediately, then registers the subflow. If this was the
// last expected subflow for its ConnectionID, a MultipathConn is pushed to
// acceptCh (§4.1).
func (l *Listener) handleSubflow(tcpConn net.Conn) {
	remote := tcpConn.RemoteAddr()

	// Enforce a deadline so a slow/abusive client cannot hold resources.
	_ = tcpConn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	pkt, err := ReadPacket(tcpConn)
	_ = tcpConn.SetReadDeadline(time.Time{})
	if err != nil {
		slog.Warn("handleSubflow: failed to read HANDSHAKE", "remote", remote, "err", err)
		_ = tcpConn.Close()
		return
	}

	// Must be a HANDSHAKE packet with a valid checksum and NumSubflows byte.
	if pkt.Type != TypeHandshake || len(pkt.Data) < 1 {
		slog.Warn("handleSubflow: unexpected packet type or missing data",
			"remote", remote,
			"type", fmt.Sprintf("0x%02x", pkt.Type),
		)
		_ = tcpConn.Close()
		return
	}

	if !VerifyChecksum(pkt) {
		slog.Warn("handleSubflow: checksum verification failed", "remote", remote)
		_ = tcpConn.Close()
		return
	}

	connID := pkt.ConnectionID
	sfIdx := int(pkt.SubflowIndex)
	numExpected := int(pkt.Data[0])

	slog.Debug("handleSubflow: received HANDSHAKE",
		"remote", remote,
		"connID", connIDStr(connID),
		"sfIdx", sfIdx,
		"numExpected", numExpected,
	)

	// Basic sanity: numExpected in [1,255], sfIdx within range.
	if numExpected < 1 || numExpected > 255 || sfIdx >= numExpected {
		slog.Warn("handleSubflow: invalid HANDSHAKE parameters",
			"remote", remote,
			"connID", connIDStr(connID),
			"sfIdx", sfIdx,
			"numExpected", numExpected,
		)
		_ = tcpConn.Close()
		return
	}

	lAddr := tcpConn.LocalAddr().(*net.TCPAddr)
	rAddr := tcpConn.RemoteAddr().(*net.TCPAddr)

	sf := &Subflow{
		Index:      uint8(sfIdx),
		TCPConn:    tcpConn,
		LocalAddr:  lAddr.IP,
		RemoteAddr: rAddr.IP,
		State:      SubflowActive,
	}

	// Send HANDSHAKE_ACK — confirms this subflow is accepted (§5.2 type 0x02).
	ackPkt := &Packet{
		Version:      ProtocolVersion,
		Type:         TypeHandshakeAck,
		SubflowIndex: uint8(sfIdx),
		ConnectionID: connID,
		Timestamp:    uint64(time.Now().UnixMicro()),
	}
	if err := WritePacket(tcpConn, ackPkt); err != nil {
		slog.Error("handleSubflow: failed to send HANDSHAKE_ACK",
			"remote", remote,
			"connID", connIDStr(connID),
			"sfIdx", sfIdx,
			"err", err,
		)
		_ = tcpConn.Close()
		return
	}
	slog.Debug("handleSubflow: sent HANDSHAKE_ACK",
		"remote", remote,
		"connID", connIDStr(connID),
		"sfIdx", sfIdx,
	)

	// Register and check if the connection group is complete.
	conn := l.registerSubflow(connID, sf, numExpected)
	if conn != nil {
		select {
		case l.acceptCh <- conn:
		case <-l.closed:
		}
	}
}

// registerSubflow inserts sf into the pending group for connID.
// Returns a fully-assembled MultipathConn when all subflows have arrived,
// or nil if more are still expected.
func (l *Listener) registerSubflow(connID [16]byte, sf *Subflow, numExpected int) *MultipathConn {
	l.mu.Lock()
	defer l.mu.Unlock()

	pc, exists := l.pending[connID]
	if !exists {
		slog.Debug("registerSubflow: new connection group",
			"connID", connIDStr(connID),
			"numExpected", numExpected,
		)
		pc = &pendingConn{
			subflows:    make([]*Subflow, numExpected),
			numExpected: numExpected,
		}
		// If the group does not complete in time, clean up the partial state.
		pc.timer = time.AfterFunc(pendingConnTimeout, func() {
			l.expirePending(connID)
		})
		l.pending[connID] = pc
	}

	// Guard against a duplicate or out-of-range index from a misbehaving client.
	if sf.Index >= uint8(pc.numExpected) || pc.subflows[sf.Index] != nil {
		slog.Warn("registerSubflow: duplicate or out-of-range subflow index",
			"connID", connIDStr(connID),
			"sfIdx", sf.Index,
		)
		return nil
	}

	pc.subflows[sf.Index] = sf
	pc.arrived++

	slog.Info("registerSubflow: subflow registered",
		"connID", connIDStr(connID),
		"sfIdx", sf.Index,
		"arrived", pc.arrived,
		"expected", pc.numExpected,
	)

	if pc.arrived < pc.numExpected {
		slog.Debug("registerSubflow: waiting for more subflows",
			"connID", connIDStr(connID),
			"arrived", pc.arrived,
			"expected", pc.numExpected,
		)
		return nil
	}

	// All subflows present — stop the timeout and assemble.
	pc.timer.Stop()
	delete(l.pending, connID)

	slog.Info("registerSubflow: all subflows arrived, assembling MultipathConn",
		"connID", connIDStr(connID),
		"numSubflows", numExpected,
	)
	return NewMultipathConn(connID, pc.subflows, l.sched)
}

// expirePending closes the TCP connections belonging to a timed-out partial
// connection and removes it from the pending map.
func (l *Listener) expirePending(connID [16]byte) {
	l.mu.Lock()
	pc, ok := l.pending[connID]
	if ok {
		delete(l.pending, connID)
	}
	l.mu.Unlock()

	if ok {
		slog.Warn("expirePending: connection group timed out, closing partial subflows",
			"connID", connIDStr(connID),
			"arrived", pc.arrived,
			"expected", pc.numExpected,
		)
		for _, sf := range pc.subflows {
			if sf != nil {
				_ = sf.TCPConn.Close()
			}
		}
	}
}
