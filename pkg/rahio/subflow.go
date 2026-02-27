package rahio

import (
	"net"
	"time"
)

// SubflowState mirrors the state machine from Section 6.1.
type SubflowState uint8

const (
	SubflowConnecting SubflowState = iota
	SubflowActive
	SubflowDegraded // RTT too high, not preferred
	SubflowClosing
	SubflowClosed
)

// Subflow represents one TCP connection within a MultipathConn.
// Modelled after the kernel's mptcp_subflow_context (Section 6.2).
type Subflow struct {
	Index      uint8
	TCPConn    net.Conn
	Interface  string
	LocalAddr  net.IP
	RemoteAddr net.IP
	State      SubflowState
	RTT        time.Duration
	BytesSent  uint64
	BytesRecv  uint64
	Scheduled  bool
}
