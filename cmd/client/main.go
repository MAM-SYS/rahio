package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/armon/go-socks5"

	rahio "github.com/hossein/rahio/pkg/rahio"
)

func main() {
	server := flag.String("server", "", "Rahio server host:port (required)")
	socksAddr := flag.String("socks", "127.0.0.1:1080", "Local SOCKS5 listen address")
	ifaces := flag.String("ifaces", "", "Comma-separated interface names, e.g. eth0,wlan0 (required)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	if *server == "" {
		slog.Error("client: --server is required")
		os.Exit(1)
	}
	if *ifaces == "" {
		slog.Error("client: --ifaces is required")
		os.Exit(1)
	}

	ifaceList := strings.Split(*ifaces, ",")
	slog.Info("client: starting",
		"server", *server,
		"socks", *socksAddr,
		"ifaces", ifaceList,
		"numSubflows", len(ifaceList),
	)

	// Pre-resolve interface addresses so problems are visible at startup.
	for i, name := range ifaceList {
		addr, err := interfaceLocalAddr(strings.TrimSpace(name))
		if err != nil {
			slog.Warn("client: interface address lookup failed (OS will choose source)",
				"iface", name,
				"idx", i,
				"err", err,
			)
		} else {
			slog.Info("client: interface resolved", "iface", name, "idx", i, "localAddr", addr)
		}
	}

	srv, err := socks5.New(&socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialRahio(*server, ifaceList, addr)
		},
	})
	if err != nil {
		slog.Error("client: socks5.New failed", "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", *socksAddr)
	if err != nil {
		slog.Error("client: failed to listen for SOCKS5", "addr", *socksAddr, "err", err)
		os.Exit(1)
	}
	slog.Info("client: SOCKS5 proxy ready", "socks", *socksAddr, "server", *server)

	if err := srv.Serve(ln); err != nil {
		slog.Error("client: SOCKS5 server error", "err", err)
		os.Exit(1)
	}
}

// dialRahio opens a Rahio MultipathConn to server, writes the dest frame for
// target ("host:port"), and returns the connection so go-socks5 can bridge it.
func dialRahio(server string, ifaceList []string, target string) (net.Conn, error) {
	slog.Info("dialRahio: new SOCKS5 CONNECT",
		"target", target,
		"server", server,
		"numSubflows", len(ifaceList),
	)

	localAddrs := make([]string, len(ifaceList))
	for i, name := range ifaceList {
		addr, err := interfaceLocalAddr(strings.TrimSpace(name))
		if err != nil {
			slog.Warn("dialRahio: interface lookup failed, OS will choose source",
				"iface", name,
				"idx", i,
				"err", err,
			)
			localAddrs[i] = ""
		} else {
			localAddrs[i] = addr
			slog.Debug("dialRahio: subflow local address",
				"idx", i,
				"iface", name,
				"localAddr", addr,
			)
		}
	}

	slog.Debug("dialRahio: dialing Rahio server",
		"server", server,
		"localAddrs", localAddrs,
	)
	mc, err := rahio.Dial(server, len(ifaceList), localAddrs, nil)
	if err != nil {
		slog.Error("dialRahio: Rahio dial failed", "server", server, "target", target, "err", err)
		return nil, fmt.Errorf("rahio dial: %w", err)
	}
	slog.Info("dialRahio: Rahio MultipathConn established",
		"target", target,
		"rahioLocal", mc.LocalAddr(),
		"rahioRemote", mc.RemoteAddr(),
	)

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
	atypName := map[byte]string{0x01: "IPv4", 0x03: "domain", 0x04: "IPv6"}[atyp]
	slog.Debug("dialRahio: writing dest frame",
		"target", target,
		"atyp", fmt.Sprintf("0x%02x (%s)", atyp, atypName),
		"addrLen", len(addr),
		"port", port64,
	)

	if err := writeDestFrame(mc, atyp, addr, uint16(port64)); err != nil {
		mc.Close()
		slog.Error("dialRahio: writeDestFrame failed", "target", target, "err", err)
		return nil, fmt.Errorf("writeDestFrame: %w", err)
	}

	slog.Info("dialRahio: ready, handing off to bridge", "target", target)
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
