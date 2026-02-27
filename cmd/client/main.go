package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/armon/go-socks5"

	"github.com/hossein/rahio/pkg/rahio"
)

func main() {
	server := flag.String("server", "", "Rahio server host:port (required)")
	socksAddr := flag.String("socks", "127.0.0.1:1080", "Local SOCKS5 listen address")
	ifaces := flag.String("ifaces", "", "Comma-separated interface names, e.g. eth0,wlan0 (required)")
	flag.Parse()

	if *server == "" {
		log.Fatal("--server is required")
	}
	if *ifaces == "" {
		log.Fatal("--ifaces is required")
	}

	ifaceList := strings.Split(*ifaces, ",")
	srv, err := socks5.New(&socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialRahio(*server, ifaceList, addr)
		},
	})
	if err != nil {
		log.Fatalf("socks5.New: %v", err)
	}

	ln, err := net.Listen("tcp", *socksAddr)
	if err != nil {
		log.Fatalf("listen socks5: %v", err)
	}
	log.Printf("SOCKS5 proxy listening on %s → rahio server %s (ifaces: %v)", *socksAddr, *server, ifaceList)

	if err := srv.Serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// dialRahio opens a Rahio MultipathConn to server, writes the dest frame for
// target ("host:port"), and returns the connection so go-socks5 can bridge it.
func dialRahio(server string, ifaceList []string, target string) (net.Conn, error) {
	localAddrs := make([]string, len(ifaceList))
	for i, name := range ifaceList {
		addr, err := interfaceLocalAddr(strings.TrimSpace(name))
		if err != nil {
			log.Printf("iface %s: %v (OS will choose)", name, err)
		}

		localAddrs[i] = addr // "" is fine — dialSubflow skips LocalAddr when empty
	}

	mc, err := rahio.Dial(server, len(ifaceList), localAddrs, nil)
	if err != nil {
		return nil, fmt.Errorf("rahio dial: %w", err)
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		mc.Close()

		return nil, fmt.Errorf("parsing target %q: %w", target, err)
	}

	port64, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		mc.Close()

		return nil, fmt.Errorf("parsing port %q: %w", portStr, err)
	}

	atyp, addr := classifyAddr(host)
	if err := writeDestFrame(mc, atyp, addr, uint16(port64)); err != nil {
		mc.Close()

		return nil, fmt.Errorf("writeDestFrame: %w", err)
	}

	return mc, nil
}

// classifyAddr returns the SOCKS5 ATYP byte and raw address bytes for host.
func classifyAddr(host string) (atyp byte, addr []byte) {
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return 0x01, ip4
		}

		return 0x04, ip.To16()
	}

	return 0x03, []byte(host)
}

// interfaceLocalAddr returns the first IPv4 address of the named interface
// in "ip:0" form suitable as a local dial address.
func interfaceLocalAddr(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", fmt.Errorf("interface %q not found: %w", name, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("listing addrs for %q: %w", name, err)
	}

	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String() + ":0", nil
		}
	}

	return "", fmt.Errorf("no IPv4 address on interface %q", name)
}

// writeDestFrame writes the destination address frame to the Rahio connection.
// Format: [ATYP][len?][addr][port_hi][port_lo]
// For ATYP=0x03 (domain), a 1-byte length prefix is inserted before addr.
func writeDestFrame(w io.Writer, atyp byte, addr []byte, port uint16) error {
	var buf []byte
	buf = append(buf, atyp)
	if atyp == 0x03 {
		buf = append(buf, byte(len(addr)))
	}

	buf = append(buf, addr...)
	buf = append(buf, byte(port>>8), byte(port&0xFF))
	_, err := w.Write(buf)

	return err
}
