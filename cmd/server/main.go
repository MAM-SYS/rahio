package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/hossein/rahio/internal/proxy"
	"github.com/hossein/rahio/pkg/rahio"
)

func main() {
	listen := flag.String("listen", ":9000", "Rahio listen address")
	flag.Parse()

	ln, err := rahio.Listen(*listen, nil)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("rahio server listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()

	target, err := readDestFrame(conn)
	if err != nil {
		log.Printf("readDestFrame: %v", err)
		return
	}

	remote, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("dial %s: %v", target, err)
		return
	}
	defer remote.Close()

	proxy.Bridge(conn, remote)
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

	var host string
	switch atyp[0] {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", fmt.Errorf("reading IPv4: %w", err)
		}
		host = net.IP(b).String()

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

	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", fmt.Errorf("reading IPv6: %w", err)
		}
		host = "[" + net.IP(b).String() + "]"

	default:
		return "", fmt.Errorf("unknown ATYP 0x%02x", atyp[0])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(r, portBuf); err != nil {
		return "", fmt.Errorf("reading port: %w", err)
	}
	port := binary.BigEndian.Uint16(portBuf)

	return fmt.Sprintf("%s:%d", host, port), nil
}
