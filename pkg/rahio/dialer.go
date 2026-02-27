package rahio

import (
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hossein/rahio/pkg/rahio/scheduler"
)

const handshakeTimeout = 10 * time.Second

// Dial establishes a MultipathConn to addr using numSubflows TCP connections (§4.1).
//
// All subflows connect to the same addr (same host:port), each carrying a
// HANDSHAKE packet that includes the shared ConnectionID and the subflow's
// index. The server groups them by ConnectionID; once all arrive it calls
// Accept().
//
// localAddrs optionally pins each subflow to a specific local interface address
// (e.g. "192.168.1.5:0"). An empty string or a slice shorter than numSubflows
// leaves the OS to choose the source address for the remaining subflows.
//
// sched selects which subflow each chunk is sent on; pass nil to use
// round-robin (§7.3).
func Dial(addr string, numSubflows int, localAddrs []string, sched scheduler.SchedulerOps) (*MultipathConn, error) {
	if numSubflows < 1 || numSubflows > 255 {
		return nil, fmt.Errorf("rahio: numSubflows must be 1-255, got %d", numSubflows)
	}

	// Random 16-byte ConnectionID ties all subflows together (§4.1).
	var connID [16]byte
	if _, err := rand.Read(connID[:]); err != nil {
		return nil, fmt.Errorf("rahio: generating connection ID: %w", err)
	}

	if sched == nil {
		sched = scheduler.NewRoundRobin()
	}

	// Dial all subflows in parallel so the server sees them close together.
	type result struct {
		sf  *Subflow
		err error
	}
	results := make([]result, numSubflows)
	var wg sync.WaitGroup
	for i := 0; i < numSubflows; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sf, err := dialSubflow(addr, idx, numSubflows, connID, localAddrs)
			results[idx] = result{sf, err}
		}(i)
	}
	wg.Wait()

	// Collect results; on any error close all successfully opened subflows.
	subflows := make([]*Subflow, numSubflows)
	var firstErr error
	for i, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}

		subflows[i] = r.sf
	}

	if firstErr != nil {
		for _, sf := range subflows {
			if sf != nil {
				_ = sf.TCPConn.Close()
			}
		}

		return nil, firstErr
	}

	return NewMultipathConn(connID, subflows, sched), nil
}

// dialSubflow handles the full TCP dial → HANDSHAKE → HANDSHAKE_ACK exchange
// for a single subflow (§4.1).
func dialSubflow(addr string, idx, numSubflows int, connID [16]byte, localAddrs []string) (*Subflow, error) {
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	if idx < len(localAddrs) && localAddrs[idx] != "" {
		localTCP, err := net.ResolveTCPAddr("tcp", localAddrs[idx])
		if err != nil {

			return nil, fmt.Errorf("rahio: resolving local addr %q for subflow %d: %w", localAddrs[idx], idx, err)
		}

		dialer.LocalAddr = localTCP
	}

	tcpConn, err := dialer.Dial("tcp", addr)
	if err != nil {

		return nil, fmt.Errorf("rahio: dialing subflow %d: %w", idx, err)
	}

	// HANDSHAKE packet: header carries Version, Type, SubflowIndex, ConnectionID.
	// Data[0] = NumSubflows — the only field from §4.1 not already in the header.
	hsPkt := &Packet{
		Version:      ProtocolVersion,
		Type:         TypeHandshake,
		SubflowIndex: uint8(idx),
		ConnectionID: connID,
		Timestamp:    uint64(time.Now().UnixMicro()),
		Data:         []byte{uint8(numSubflows)},
	}
	if err = WritePacket(tcpConn, hsPkt); err != nil {
		_ = tcpConn.Close()

		return nil, fmt.Errorf("rahio: sending HANDSHAKE for subflow %d: %w", idx, err)
	}

	// Wait for HANDSHAKE_ACK (§5.2 type 0x02).
	_ = tcpConn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	ack, err := ReadPacket(tcpConn)
	_ = tcpConn.SetReadDeadline(time.Time{}) // clear deadline for data phase
	if err != nil {
		_ = tcpConn.Close()

		return nil, fmt.Errorf("rahio: reading HANDSHAKE_ACK for subflow %d: %w", idx, err)
	}

	if ack.Type != TypeHandshakeAck {
		_ = tcpConn.Close()

		return nil, fmt.Errorf("rahio: subflow %d: expected HANDSHAKE_ACK (0x02), got 0x%02x", idx, ack.Type)
	}

	if ack.ConnectionID != connID {
		_ = tcpConn.Close()

		return nil, fmt.Errorf("rahio: subflow %d: HANDSHAKE_ACK connection ID mismatch", idx)
	}

	lAddr := tcpConn.LocalAddr().(*net.TCPAddr)
	rAddr := tcpConn.RemoteAddr().(*net.TCPAddr)

	return &Subflow{
		Index:      uint8(idx),
		TCPConn:    tcpConn,
		LocalAddr:  lAddr.IP,
		RemoteAddr: rAddr.IP,
		State:      SubflowActive,
	}, nil
}
