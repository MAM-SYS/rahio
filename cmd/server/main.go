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
	chunkSize := flag.Int("chunk", 32*1024, "Chunk size")
	recvWindow := flag.Uint("recv", rahio.DefaultRecvWindow, "Flow control receive Window size")
	sendWindow := flag.Int("send", rahio.DefaultSendWindow, "Flow control send Window size")
	handshakeTimout := flag.Duration("handshake", rahio.DefaultHandshakeTimeout, "Handshake timeout")
	pendingConnTimeout := flag.Duration("pending", rahio.DefaultPendingConnTimeout, "Pending connection timeout")
	flag.Parse()

	cfg := rahio.NewListenerCfg(*chunkSize, uint32(*recvWindow), int64(*sendWindow), *handshakeTimout, *pendingConnTimeout)
	ln, err := rahio.NewListener(*listen, nil, cfg)
	if err != nil {
		slog.Error("server: failed to listen", "err", err)
		os.Exit(1)
	}

	slog.Info("server: ready",
		"addr", ln.Addr(),
		"chunkSize", *chunkSize,
	)
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
