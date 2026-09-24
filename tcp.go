package main

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

const (
	tcpDialTimeout = 5 * time.Second
	tcpKeepAlive   = 15 * time.Second
)

var tcpDialer = net.Dialer{Timeout: tcpDialTimeout, KeepAlive: tcpKeepAlive}

type tcpProxy struct {
	base
	ln net.Listener
}

func startTCP(r *Rule, idx int, m Mapping) (*tcpProxy, error) {
	p := &tcpProxy{base: newBase("tcp", r, idx, m)}
	lc := net.ListenConfig{KeepAlive: tcpKeepAlive}
	ln, err := lc.Listen(context.Background(), "tcp", p.listen)
	if err != nil {
		return nil, err
	}
	p.ln = ln
	p.log.Info("TCP proxy started")
	go p.serve()
	return p, nil
}

func (p *tcpProxy) Close() error { return p.ln.Close() }

func (p *tcpProxy) serve() {
	var backoff time.Duration
	for {
		c, err := p.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Typically EMFILE: back off instead of spinning.
			backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
			p.errors.Add(1)
			p.log.Warn("accept failed", "err", err, "retryIn", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		go p.handle(c)
	}
}

func (p *tcpProxy) handle(client net.Conn) {
	defer client.Close()
	p.total.Add(1)
	p.active.Add(1)
	defer p.active.Add(-1)

	target, err := tcpDialer.Dial("tcp", p.target)
	if err != nil {
		p.errors.Add(1)
		p.log.Warn("dial target failed", "client", client.RemoteAddr().String(), "err", err)
		return
	}
	defer target.Close()
	p.log.Debug("TCP connection established", "client", client.RemoteAddr().String())

	done := make(chan struct{})
	go func() {
		p.bytesUp.Add(p.pipe(target, client))
		close(done)
	}()
	p.bytesDown.Add(p.pipe(client, target))
	<-done
	p.log.Debug("TCP connection closed", "client", client.RemoteAddr().String())
}

// pipe copies src to dst. On *net.TCPConn io.Copy uses splice(2) on Linux,
// so payload never enters user space. A clean EOF is propagated as a
// half-close; an error tears down both sides so the peer copy unblocks.
func (p *tcpProxy) pipe(dst, src net.Conn) int64 {
	n, err := io.Copy(dst, src)
	if err == nil {
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			if cw.CloseWrite() == nil {
				return n
			}
		}
	} else if !errors.Is(err, net.ErrClosed) {
		p.errors.Add(1)
		p.log.Debug("TCP pipe error", "err", err)
	}
	dst.Close()
	src.Close()
	return n
}
