package proxy

import (
	"errors"
	"io"
	"net"
	"sync"
)

// maxChunk bounds one copy step so counters stay live on long transfers.
const maxChunk = 256 << 10

var bufPool = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

// copyConn moves src to dst until EOF, accounting every step on c. On Linux it
// uses splice(2) so payload never enters user space; elsewhere, or if splice
// is unavailable, it falls back to a pooled buffer.
func copyConn(c *Conn, dst, src *net.TCPConn, up bool) error {
	if handled, err := spliceCopy(c, dst, src, up); handled {
		return err
	}
	return bufCopy(c, dst, src, up)
}

func bufCopy(c *Conn, dst io.Writer, src io.Reader, up bool) error {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	for {
		n, err := src.Read(buf[:min(len(buf), c.chunk())])
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if aerr := c.account(n, up); aerr != nil {
				return aerr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
