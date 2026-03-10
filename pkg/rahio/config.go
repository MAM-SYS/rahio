package rahio

import (
	"time"
)

const (
	DefaultChunkSize          = 32 * 1024       // 32 KB per chunk
	DefaultRecvWindow         = 4 * 1024 * 1024 // 4 MB — our receive capacity, advertised to the peer
	DefaultSendWindow         = 4 * 1024 * 1024 // 4 MB — optimistic initial send window before first ACK
	DefaultHandshakeTimeout   = 10 * time.Second
	DefaultPendingConnTimeout = 30 * time.Second
)

type ConnCfg struct {
	ChunkSize  int
	RecvWindow uint32
	SendWindow int64
}

func NewConnCfg(chunkSize int, recvWindow uint32, sendWindow int64) *ConnCfg {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}

	if recvWindow <= 0 {
		recvWindow = DefaultRecvWindow
	}

	if sendWindow <= 0 {
		sendWindow = DefaultSendWindow
	}

	return &ConnCfg{
		ChunkSize:  chunkSize,
		RecvWindow: recvWindow,
		SendWindow: sendWindow,
	}
}

type DialerCfg struct {
	HandshakeTimeout time.Duration
	ConnCfg          *ConnCfg
}

func NewDialerCfg(chunkSize int, recvWindow uint32, sendWindow int64, handshakeTimeout time.Duration) *DialerCfg {
	if handshakeTimeout <= 0 {
		handshakeTimeout = DefaultHandshakeTimeout
	}

	return &DialerCfg{
		HandshakeTimeout: handshakeTimeout,
		ConnCfg:          NewConnCfg(chunkSize, recvWindow, sendWindow),
	}
}

type ListenerCfg struct {
	ConnCfg            *ConnCfg
	HandshakeTimeout   time.Duration
	PendingConnTimeout time.Duration
}

func NewListenerCfg(chunkSize int, recvWindow uint32, sendWindow int64, handshakeTimeout time.Duration, pendingConnTimeout time.Duration) *ListenerCfg {
	if handshakeTimeout <= 0 {
		handshakeTimeout = DefaultHandshakeTimeout
	}

	if pendingConnTimeout <= 0 {
		pendingConnTimeout = DefaultPendingConnTimeout
	}

	return &ListenerCfg{
		HandshakeTimeout:   handshakeTimeout,
		PendingConnTimeout: pendingConnTimeout,
		ConnCfg:            NewConnCfg(chunkSize, recvWindow, sendWindow),
	}
}
