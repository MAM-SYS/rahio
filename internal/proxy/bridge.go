package proxy

import "io"

// Bridge copies bytes between a and b in both directions.
// Returns when one direction reaches EOF or errors.
func Bridge(a, b io.ReadWriter) {
	done := make(chan struct{}, 2)
	cp := func(dst io.Writer, src io.Reader) { io.Copy(dst, src); done <- struct{}{} }
	go cp(a, b)
	go cp(b, a)
	<-done
}
