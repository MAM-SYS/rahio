package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/hossein/rahio/internal/proxy"
	rahio "github.com/hossein/rahio/pkg/rahio"
)

func main() {
	listen := flag.String("listen", ":9000", "Rahio listen address")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	slog.Info("server: starting", "listen", *listen)

	ln, err := rahio.Listen(*listen, nil)
	if err != nil {
		slog.Error("server: failed to listen", "err", err)
		os.Exit(1)
	}
	slog.Info("server: ready", "addr", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			slog.Error("server: Accept failed, shutting down", "err", err)
			return
		}
		slog.Info("server: new MultipathConn accepted",
			"remote", conn.RemoteAddr(),
			"local", conn.LocalAddr(),
		)
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	remote := conn.RemoteAddr()
	slog.Info("handleConn: started", "remote", remote)
	defer func() {
		conn.Close()
		slog.Info("handleConn: connection closed", "remote", remote)
	}()

	target, err := readDestFrame(conn)
	if err != nil {
		slog.Error("handleConn: failed to read dest frame", "remote", remote, "err", err)
		return
	}
	slog.Info("handleConn: destination resolved", "remote", remote, "target", target)

	remote2, err := net.Dial("tcp", target)
	if err != nil {
		slog.Error("handleConn: failed to dial target", "remote", remote, "target", target, "err", err)
		return
	}
	defer remote2.Close()

	slog.Info("handleConn: dialed target, starting bridge",
		"client", remote,
		"target", target,
		"targetLocal", remote2.LocalAddr(),
	)

	proxy.Bridge(conn, remote2)

	slog.Info("handleConn: bridge done", "remote", remote, "target", target)
}

// readDestFrame reads the destination address frame written by the client.
// Frame format: [ATYP][addr...][port_hi][port_lo]
//   - 0x01 IPv4:   4 bytes
//   - 0x03 domain: 1-byte length + N bytes
//   - 0x04 IPv6:   16 bytes
func readDestFrame(r io.Reader) (string, error) {
	atyp := make([]byte, 1)
	if _, err := io.ReadFull(r, atyp); err != nil {
		return "", fmt.Errorf("reading ATYP: %w", err)
	}

	slog.Debug("readDestFrame: ATYP", "atyp", fmt.Sprintf("0x%02x", atyp[0]))

	var host string
	switch atyp[0] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", fmt.Errorf("reading IPv4: %w", err)
		}
		host = net.IP(b).String()
		slog.Debug("readDestFrame: IPv4 address", "ip", host)

	case 0x03: // domain
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return "", fmt.Errorf("reading domain length: %w", err)
		}
		domain := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(r, domain); err != nil {
			return "", fmt.Errorf("reading domain: %w", err)
		}
		host = string(domain)
		slog.Debug("readDestFrame: domain", "domain", host)

	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", fmt.Errorf("reading IPv6: %w", err)
		}
		host = "[" + net.IP(b).String() + "]"
		slog.Debug("readDestFrame: IPv6 address", "ip", host)

	default:
		return "", fmt.Errorf("unknown ATYP 0x%02x", atyp[0])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(r, portBuf); err != nil {
		return "", fmt.Errorf("reading port: %w", err)
	}
	port := binary.BigEndian.Uint16(portBuf)

	addr := fmt.Sprintf("%s:%d", host, port)
	slog.Debug("readDestFrame: complete", "addr", addr)
	return addr, nil
}
