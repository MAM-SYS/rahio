package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/hossein/rahio/internal/proxy"
	"github.com/hossein/rahio/pkg/rahio"
)

func banner() string {
	return `
██████╗  █████╗ ██╗   ██╗  ██╗ ██████╗ *
██╔══██╗██╔══██╗██║   ██║  ██║██╔═══██╗
██████╔╝███████║██║██║██║  ██║██║   ██║
██╔══██╗██╔══██║██╔═══██║  ██║██║   ██║
██║  ██║██║  ██║██║   ██║  ██║╚██████╔╝
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝   ╚═╝  ╚═╝ ╚═════╝  client
`
}

func main() {
	fmt.Println(banner())
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	remoteServer := flag.String("server", "", "Rahio server host:port (required)")
	localProxy := flag.String("proxy", "127.0.0.1:1080", "Local proxy listen address")
	ifaces := flag.String("ifaces", "", "Comma-separated interface names, e.g. eth0,wlan0 (required)")
	flag.Parse()
	if *remoteServer == "" {
		slog.Error("client: --server is required")
		os.Exit(1)
	}

	if *ifaces == "" {
		slog.Error("client: --ifaces is required")
		os.Exit(1)
	}

	ifaceList := strings.Split(*ifaces, ",")
	addrs := make([]string, len(ifaceList))
	for i, name := range ifaceList {
		loc, err := ifaceToLocal(strings.TrimSpace(name))
		if err != nil {
			slog.Error("client: error parsing interface name", "iface", name, "err", err)
			os.Exit(1)
		}

		addrs[i] = loc
	}

	ln, err := net.Listen("tcp", *localProxy)
	if err != nil {
		slog.Error("server: failed to listen", "err", err)
		os.Exit(1)
	}

	slog.Info("Rahio client: starting",
		"server", *remoteServer,
		"proxy", *localProxy,
		"ifaces", ifaceList,
		"numSubflows", len(ifaceList),
	)
	for {
		conn, err := ln.Accept()
		if err != nil {
			slog.Error("server: Accept failed, shutting down", "err", err)
			continue
		}

		mc, err := rahio.Dial(*remoteServer, len(ifaceList), addrs, nil)
		if err != nil {
			slog.Error("rahio: Rahio dial failed", "server", remoteServer, "err", err)
			continue
		}

		go proxy.Proxy(mc, conn)
		go proxy.Proxy(conn, mc)
	}
}

func ifaceToLocal(i string) (string, error) {
	iface, err := net.InterfaceByName(i)
	if err != nil {
		return "", fmt.Errorf("interface %q not found: %w", i, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("listing addrs for %q: %w", i, err)
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

	return "", fmt.Errorf("no IPv4 address on interface %q", i)
}
