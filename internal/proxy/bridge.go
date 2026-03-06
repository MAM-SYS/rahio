package proxy

import "io"

func Proxy(dst io.Writer, src io.Reader) {
	io.Copy(dst, src)
	if tcpConn, ok := dst.(io.WriteCloser); ok {
		tcpConn.Close()
	}
}
