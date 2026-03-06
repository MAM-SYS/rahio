package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/armon/go-socks5"
	"github.com/hossein/rahio/pkg/rahio"
)

func banner() string {
	return `
██████╗  █████╗ ██╗   ██╗  ██╗ ██████╗ *
██╔══██╗██╔══██╗██║   ██║  ██║██╔═══██╗ 
██████╔╝███████║██║██║██║  ██║██║   ██║ 
██╔══██╗██╔══██║██╔═══██║  ██║██║   ██║ 
██║  ██║██║  ██║██║   ██║  ██║╚██████╔╝ 
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝   ╚═╝  ╚═╝ ╚═════╝  server
`
}

func main() {
	fmt.Println(banner())
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	listen := flag.String("listen", ":9000", "Rahio server listen address")
	flag.Parse()
	ln, err := rahio.Listen(*listen, nil)
	if err != nil {
		slog.Error("server: failed to listen", "err", err)
		os.Exit(1)
	}

	slog.Info("server: ready", "addr", ln.Addr())
	srv, err := socks5.New(&socks5.Config{})
	if err != nil {
		slog.Error("client: socks5.New failed", "err", err)
		os.Exit(1)
	}

	if err = srv.Serve(ln); err != nil {
		slog.Error("client: SOCKS5 server error", "err", err)
		os.Exit(1)
	}
}
