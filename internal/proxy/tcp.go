package proxy

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

const (
	tcpDialTimeout = 5 * time.Second
	tcpKeepAlive   = 15 * time.Second
)

var tcpDialer = net.Dialer{Timeout: tcpDialTimeout, KeepAlive: tcpKeepAlive}

type tcpListener struct {
	rn   *Runner
	idx  int
	addr string
	ln   net.Listener
}

func (r *Runner) listenTCP(idx int, addr string) (*tcpListener, error) {
	lc := net.ListenConfig{KeepAlive: tcpKeepAlive}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	l := &tcpListener{rn: r, idx: idx, addr: addr, ln: ln}
	r.log.Debug("TCP listener started", "listen", addr)
	go l.serve()
	return l, nil
}

func (l *tcpListener) Close() error { return l.ln.Close() }

func (l *tcpListener) serve() {
	var backoff time.Duration
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Typically EMFILE: back off instead of spinning.
			backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
			l.rn.errors.Add(1)
			l.rn.log.Warn("accept failed", "listen", l.addr, "err", err, "retryIn", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		go l.handle(c.(*net.TCPConn))
	}
}

func (l *tcpListener) handle(client *net.TCPConn) {
	r := l.rn
	target := r.target(l.idx)
	var upstream atomic.Pointer[net.TCPConn]
	c := r.admit(client.RemoteAddr().(*net.TCPAddr).AddrPort(), l.addr, target, func() {
		client.Close()
		if u := upstream.Load(); u != nil {
			u.Close()
		}
	})
	if c == nil {
		client.Close()
		return
	}
	defer r.release(c)
	defer c.Close()

	tc, err := tcpDialer.DialContext(c.ctx, "tcp", target)
	if err != nil {
		if c.ctx.Err() == nil {
			r.errors.Add(1)
			r.log.Warn("dial target failed", "client", c.client.String(), "target", target, "err", err)
		}
		return
	}
	// Close cancels ctx before loading upstream, so either it sees the
	// socket or we see the cancellation here.
	up := tc.(*net.TCPConn)
	upstream.Store(up)
	if c.ctx.Err() != nil {
		up.Close()
		return
	}
	r.log.Debug("TCP connection established", "client", c.client.String(), "target", target)

	done := make(chan struct{})
	go func() {
		pipe(c, up, client, true)
		close(done)
	}()
	pipe(c, client, up, false)
	<-done
	r.log.Debug("TCP connection closed", "client", c.client.String(),
		"bytesUp", c.up.Load(), "bytesDown", c.down.Load())
}

// pipe copies src to dst. A clean EOF is propagated as a half-close; an error
// tears down both sides so the peer copy unblocks.
func pipe(c *Conn, dst, src *net.TCPConn, up bool) {
	err := copyConn(c, dst, src, up)
	if err == nil {
		if dst.CloseWrite() == nil {
			return
		}
	} else if !errors.Is(err, net.ErrClosed) && c.ctx.Err() == nil {
		c.runner.errors.Add(1)
		c.runner.log.Debug("TCP pipe error", "client", c.client.String(), "err", err)
	}
	c.Close()
}
