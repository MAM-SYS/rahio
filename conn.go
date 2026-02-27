package rahio

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hossein/rahio/scheduler"
)

const chunkSize = 32 * 1024 // 32 KB per chunk

var ErrNoSubflows = errors.New("rahio: no active subflows")
var ErrClosed = errors.New("rahio: connection closed")

// MultipathConn is the core of Rahio. It implements net.Conn over N subflows.
// It also implements scheduler.ConnectionInfo so the scheduler can call back
// without creating an import cycle.
type MultipathConn struct {
	ConnectionID [16]byte
	Subflows     []*Subflow
	sched        scheduler.SchedulerOps
	sendSeq      atomic.Uint64 // monotonic send sequence counter (§8.1)
	mu           sync.Mutex    // protects Subflows slice
	reassembly   *reassemblyBuffer
	closeOnce    sync.Once
	closed       chan struct{}
}

// NewMultipathConn creates a MultipathConn and starts the receive goroutines.
func NewMultipathConn(id [16]byte, subflows []*Subflow, sched scheduler.SchedulerOps) *MultipathConn {
	c := &MultipathConn{
		ConnectionID: id,
		Subflows:     subflows,
		sched:        sched,
		reassembly:   newReassemblyBuffer(),
		closed:       make(chan struct{}),
	}
	sched.Init(c)
	for _, sf := range subflows {
		go c.recvLoop(sf)
	}

	return c
}

// ── scheduler.ConnectionInfo interface ───────────────────────────────────────

func (c *MultipathConn) GetConnectionID() [16]byte {
	return c.ConnectionID
}

func (c *MultipathConn) GetSubflowCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Subflows)
}

func (c *MultipathConn) MarkSubflowScheduled(index int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index >= 0 && index < len(c.Subflows) {
		c.Subflows[index].Scheduled = true
	}
}

// ── net.Conn: Write (send path §3) ───────────────────────────────────────────

// Write splits data into chunks, assigns sequence numbers, and distributes
// them across subflows via the scheduler (§3 send path).
func (c *MultipathConn) Write(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, ErrClosed
	default:
	}

	total := 0
	for len(b) > 0 {
		n := len(b)
		if n > chunkSize {
			n = chunkSize
		}
		chunk := b[:n]
		b = b[n:]
		seq := c.sendSeq.Add(1) - 1
		c.mu.Lock()
		idx := c.sched.SelectSubflow(c)
		if idx < 0 || idx >= len(c.Subflows) {
			c.mu.Unlock()

			return total, ErrNoSubflows
		}

		sf := c.Subflows[idx]
		sf.Scheduled = false // clear after selection
		c.mu.Unlock()
		pkt := &Packet{
			Version:        ProtocolVersion,
			Type:           TypeData,
			SubflowIndex:   sf.Index,
			ConnectionID:   c.ConnectionID,
			SequenceNumber: seq,
			Timestamp:      uint64(time.Now().UnixMicro()),
			Data:           chunk,
		}
		if err := WritePacket(sf.TCPConn, pkt); err != nil {
			return total, err
		}

		sf.BytesSent += uint64(n)
		total += n
	}

	return total, nil
}

// ── net.Conn: Read (receive path §3) ─────────────────────────────────────────

// Read delivers in-order application bytes from the reassembly buffer.
func (c *MultipathConn) Read(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, io.EOF
	case data, ok := <-c.reassembly.output:
		if !ok {
			return 0, io.EOF
		}

		n := copy(b, data)

		return n, nil
	}
}

// recvLoop reads packets from one subflow and feeds them into the reassembly
// buffer. One goroutine per subflow (§3 receive path).
func (c *MultipathConn) recvLoop(sf *Subflow) {
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		pkt, err := ReadPacket(sf.TCPConn)
		if err != nil {
			c.mu.Lock()
			sf.State = SubflowClosed
			c.mu.Unlock()
			return
		}

		if !VerifyChecksum(pkt) {
			continue
		}

		sf.BytesRecv += uint64(pkt.DataLength)

		switch pkt.Type {
		case TypeData:
			c.reassembly.insert(pkt.SequenceNumber, pkt.Data)
		case TypeClose:
			_ = c.Close()
			return
		}
	}
}

// ── net.Conn: Close (§4.3) ───────────────────────────────────────────────────

func (c *MultipathConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.sched.Release(c)

		c.mu.Lock()
		defer c.mu.Unlock()

		closePkt := &Packet{
			Version:      ProtocolVersion,
			Type:         TypeClose,
			ConnectionID: c.ConnectionID,
			Timestamp:    uint64(time.Now().UnixMicro()),
		}
		for _, sf := range c.Subflows {
			if sf.State == SubflowActive {
				sf.State = SubflowClosing
				_ = WritePacket(sf.TCPConn, closePkt)
				_ = sf.TCPConn.Close()
				sf.State = SubflowClosed
			}
		}
		c.reassembly.close()
	})
	return nil
}

// ── net.Conn: addr / deadline stubs ──────────────────────────────────────────

func (c *MultipathConn) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sf := range c.Subflows {
		if sf.State == SubflowActive {
			return sf.TCPConn.LocalAddr()
		}
	}
	return nil
}

func (c *MultipathConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sf := range c.Subflows {
		if sf.State == SubflowActive {
			return sf.TCPConn.RemoteAddr()
		}
	}
	return nil
}

func (c *MultipathConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for _, sf := range c.Subflows {
		if err := sf.TCPConn.SetDeadline(t); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *MultipathConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for _, sf := range c.Subflows {
		if err := sf.TCPConn.SetReadDeadline(t); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *MultipathConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for _, sf := range c.Subflows {
		if err := sf.TCPConn.SetWriteDeadline(t); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ── Reassembly Buffer (§8.2) ─────────────────────────────────────────────────

type reassemblyBuffer struct {
	mu           sync.Mutex
	nextExpected uint64
	buffer       map[uint64][]byte
	output       chan []byte
}

func newReassemblyBuffer() *reassemblyBuffer {
	return &reassemblyBuffer{
		buffer: make(map[uint64][]byte),
		output: make(chan []byte, 256),
	}
}

// insert implements the algorithm from §8.2.
func (rb *reassemblyBuffer) insert(seq uint64, data []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if seq == rb.nextExpected {
		rb.output <- data
		rb.nextExpected++

		// Flush any consecutive buffered packets.
		for {
			d, ok := rb.buffer[rb.nextExpected]
			if !ok {
				break
			}
			rb.output <- d
			delete(rb.buffer, rb.nextExpected)
			rb.nextExpected++
		}
	} else if seq > rb.nextExpected {
		rb.buffer[seq] = data
	}
	// seq < nextExpected: duplicate/old — discard
}

func (rb *reassemblyBuffer) close() {
	close(rb.output)
}
