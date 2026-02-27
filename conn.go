package rahio

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hossein/rahio/scheduler"
)

const (
	chunkSize         = 32 * 1024       // 32 KB per chunk
	defaultRecvWindow = 4 * 1024 * 1024 // 4 MB — our receive capacity, advertised to the peer
	defaultSendWindow = 4 * 1024 * 1024 // 4 MB — optimistic initial send window before first ACK
)

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

	// Flow control — send side (§9.2)
	// fcMu protects sendWindow, inFlight, and sentPackets.
	fcMu        sync.Mutex
	fcCond      *sync.Cond        // signalled when sendWindow opens or conn closes
	sendWindow  int64             // bytes we are allowed to have in flight (peer-advertised)
	inFlight    int64             // bytes sent but not yet acknowledged
	sentPackets map[uint64]uint32 // seqNum → DataLength for unacknowledged sent chunks

	// Flow control — receive side (§9.1)
	recvWindowBytes uint32       // our receive capacity, advertised in ACK packets
	recvBufBytes    atomic.Int64 // bytes currently held in reassembly buffer / output channel
}

// NewMultipathConn creates a MultipathConn and starts the receive goroutines.
func NewMultipathConn(id [16]byte, subflows []*Subflow, sched scheduler.SchedulerOps) *MultipathConn {
	c := &MultipathConn{
		ConnectionID:    id,
		Subflows:        subflows,
		sched:           sched,
		reassembly:      newReassemblyBuffer(),
		closed:          make(chan struct{}),
		sendWindow:      defaultSendWindow,
		sentPackets:     make(map[uint64]uint32),
		recvWindowBytes: defaultRecvWindow,
	}
	c.fcCond = sync.NewCond(&c.fcMu)
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

// ── net.Conn: Write (send path §3, flow control §9.2) ────────────────────────

// Write splits data into chunks, assigns sequence numbers, and distributes
// them across subflows via the scheduler (§3 send path).
// It blocks when BytesInFlight >= SendWindow (§9.2).
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

		// §9.2: Block until there is room in the send window.
		c.fcMu.Lock()
		for c.inFlight+int64(n) > c.sendWindow {
			c.fcCond.Wait()
			// Re-check closed after waking.
			select {
			case <-c.closed:
				c.fcMu.Unlock()

				return total, ErrClosed
			default:
			}
		}
		c.inFlight += int64(n)
		c.sentPackets[seq] = uint32(n)
		c.fcMu.Unlock()

		// Select subflow via scheduler.
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

// ── net.Conn: Read (receive path §3, flow control §9.1) ──────────────────────

// Read delivers in-order application bytes from the reassembly buffer.
// Each call decrements recvBufBytes so the advertised window grows again.
func (c *MultipathConn) Read(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, io.EOF
	case data, ok := <-c.reassembly.output:
		if !ok {
			return 0, io.EOF
		}

		n := copy(b, data)
		// Application consumed len(data) bytes; reclaim that space in our window.
		c.recvBufBytes.Add(-int64(len(data)))
		return n, nil
	}
}

// ── Receive loop (one goroutine per subflow) ──────────────────────────────────

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
			// Insert into reassembly and account for received bytes.
			c.recvBufBytes.Add(int64(pkt.DataLength))
			c.reassembly.insert(pkt.SequenceNumber, pkt.Data)
			// Send ACK back to the peer on this subflow (§9.1).
			c.sendAck(sf)

		case TypeAck:
			// Peer acknowledged our data; update send-side flow control (§9.2).
			c.handleAck(pkt)

		case TypeClose:
			_ = c.Close()
			return
		}
	}
}

// sendAck sends a TypeAck packet back on sf carrying:
//   - SequenceNumber = highest contiguously delivered seq (§9.1 AckedSeqNum)
//   - Data[0:4]     = AdvertisedWindow (uint32 big-endian) — remaining receive capacity
func (c *MultipathConn) sendAck(sf *Subflow) {
	ackedSeq := c.reassembly.highestContiguous()

	available := int64(c.recvWindowBytes) - c.recvBufBytes.Load()
	if available < 0 {
		available = 0
	}

	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, uint32(available))
	ack := &Packet{
		Version:        ProtocolVersion,
		Type:           TypeAck,
		ConnectionID:   c.ConnectionID,
		SequenceNumber: ackedSeq,
		Timestamp:      uint64(time.Now().UnixMicro()),
		Data:           data,
	}
	_ = WritePacket(sf.TCPConn, ack)
}

// handleAck processes an incoming TypeAck packet and updates the send window.
//
// AckedSeqNum  = pkt.SequenceNumber  — all seq ≤ this are confirmed received
// SendWindow   = binary.BigEndian.Uint32(pkt.Data[0:4])
func (c *MultipathConn) handleAck(pkt *Packet) {
	if len(pkt.Data) < 4 {
		return
	}

	advertisedWindow := int64(binary.BigEndian.Uint32(pkt.Data[:4]))
	ackedSeq := pkt.SequenceNumber

	c.fcMu.Lock()
	// Remove all acknowledged entries and reduce bytesInFlight.
	for seq, size := range c.sentPackets {
		if seq <= ackedSeq {
			c.inFlight -= int64(size)
			delete(c.sentPackets, seq)
		}
	}

	if c.inFlight < 0 {
		c.inFlight = 0 // guard against duplicate ACKs
	}

	c.sendWindow = advertisedWindow
	c.fcCond.Broadcast() // wake any Write() calls waiting for window space
	c.fcMu.Unlock()
}

// ── net.Conn: Close (§4.3) ───────────────────────────────────────────────────

func (c *MultipathConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.fcCond.Broadcast() // unblock any Write() waiting on the send window
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

// highestContiguous returns the last sequence number that has been delivered
// in order to the output channel. Used to compute AckedSeqNum in ACK packets.
// Returns 0 when nothing has been received yet (the peer ignores a zero ACK).
func (rb *reassemblyBuffer) highestContiguous() uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.nextExpected == 0 {
		return 0
	}

	return rb.nextExpected - 1
}

func (rb *reassemblyBuffer) close() {
	close(rb.output)
}
