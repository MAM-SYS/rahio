package proxy

import (
	"io"
	"log/slog"
)

func Proxy(dst io.Writer, src io.Reader) {
	n, err := io.Copy(dst, src)
	if err != nil {
		if dstConn, ok := dst.(io.Closer); ok {
			slog.Error("proxy: close destination connection due to proxy error", "error", err)
			dstConn.Close()
		}

		if srcConn, ok := src.(io.Closer); ok {
			slog.Error("proxy: close source connection due to proxy error", "error", err)
			srcConn.Close()
		}

	}

	if tcpConn, ok := dst.(io.WriteCloser); ok {
		tcpConn.Close()
	}

	slog.Debug("proxy: proxied bytes", "bytes", n)
}
