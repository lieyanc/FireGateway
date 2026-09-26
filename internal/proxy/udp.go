package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpSessionTimeout = 5 * time.Minute
	udpBufSize        = 64 * 1024
	udpSockBuf        = 4 << 20
	udpDialTimeout    = 5 * time.Second
)

type udpListener struct {
	rn     *Runner
	idx    int
	addr   string
	conn   *net.UDPConn
	mu     sync.RWMutex
	sess   map[netip.AddrPort]*udpSession
	closed atomic.Bool
}

// udpSession is a per-client connected socket towards the target, so replies
// can be routed back to the originating client.
type udpSession struct {
	c  *Conn
	up *net.UDPConn
}

func (r *Runner) listenUDP(idx int, addr string) (*udpListener, error) {
	laddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	conn.SetReadBuffer(udpSockBuf)
	conn.SetWriteBuffer(udpSockBuf)
	l := &udpListener{rn: r, idx: idx, addr: addr, conn: conn, sess: make(map[netip.AddrPort]*udpSession)}
	r.log.Debug("UDP listener started", "listen", addr)
	go l.serve()
	return l, nil
}

func (l *udpListener) Close() error {
	l.closed.Store(true)
	err := l.conn.Close()
	l.mu.Lock()
	ss := make([]*udpSession, 0, len(l.sess))
	for _, s := range l.sess {
		ss = append(ss, s)
	}
	l.mu.Unlock()
	for _, s := range ss {
		s.c.Close()
	}
	return err
}

func (l *udpListener) serve() {
	buf := make([]byte, udpBufSize)
	for {
		n, client, err := l.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if l.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			l.rn.errors.Add(1)
			l.rn.log.Debug("UDP read failed", "listen", l.addr, "err", err)
			continue
		}
		l.forward(client, buf[:n])
	}
}

func (l *udpListener) forward(client netip.AddrPort, pkt []byte) {
	r := l.rn
	// Retry once if the session was reaped between lookup and write.
	for range 2 {
		s := l.session(client)
		if s == nil {
			return
		}
		if _, err := s.up.Write(pkt); err == nil {
			r.messages.Add(1)
			// Waiting for budget here stalls the read loop, so excess
			// datagrams are dropped by the kernel: policing, as UDP expects.
			s.c.account(len(pkt), true)
			return
		} else if !errors.Is(err, net.ErrClosed) {
			r.errors.Add(1)
			r.log.Debug("UDP forward failed", "client", client.String(), "err", err)
			return
		}
	}
}

func (l *udpListener) session(client netip.AddrPort) *udpSession {
	l.mu.RLock()
	s := l.sess[client]
	l.mu.RUnlock()
	if s != nil {
		return s
	}
	r := l.rn
	target := r.target(l.idx)
	var up atomic.Pointer[net.UDPConn]
	c := r.admit(client, l.addr, target, func() {
		if u := up.Load(); u != nil {
			u.Close()
		}
	})
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, udpDialTimeout)
	var d net.Dialer
	uc, err := d.DialContext(ctx, "udp", target)
	cancel()
	if err != nil {
		r.errors.Add(1)
		r.log.Warn("UDP dial target failed", "client", client.String(), "target", target, "err", err)
		r.release(c)
		return nil
	}
	s = &udpSession{c: c, up: uc.(*net.UDPConn)}
	s.up.SetReadBuffer(udpSockBuf)
	up.Store(s.up)
	if c.ctx.Err() != nil {
		s.up.Close()
		r.release(c)
		return nil
	}
	// Only serve() creates sessions, so no double-insert race here.
	l.mu.Lock()
	l.sess[client] = s
	l.mu.Unlock()
	r.log.Debug("UDP session created", "client", client.String(), "target", target)
	go l.reply(client, s)
	return s
}

func (l *udpListener) reply(client netip.AddrPort, s *udpSession) {
	r := l.rn
	defer func() {
		s.c.Close()
		l.mu.Lock()
		if l.sess[client] == s {
			delete(l.sess, client)
		}
		l.mu.Unlock()
		r.release(s.c)
		r.log.Debug("UDP session closed", "client", client.String(),
			"bytesUp", s.c.up.Load(), "bytesDown", s.c.down.Load())
	}()

	buf := make([]byte, udpBufSize)
	// Deadline is refreshed only on expiry rather than per packet.
	s.up.SetReadDeadline(time.Now().Add(udpSessionTimeout))
	for {
		n, err := s.up.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				idleUntil := time.Unix(0, s.c.last.Load()).Add(udpSessionTimeout)
				if time.Now().Before(idleUntil) {
					s.up.SetReadDeadline(idleUntil)
					continue
				}
				return
			}
			if !errors.Is(err, net.ErrClosed) {
				// e.g. ECONNREFUSED from an ICMP port unreachable; keep the session.
				r.errors.Add(1)
				r.log.Debug("UDP upstream read failed", "client", client.String(), "err", err)
				continue
			}
			return
		}
		if s.c.account(n, false) != nil {
			return
		}
		if _, err := l.conn.WriteToUDPAddrPort(buf[:n], client); err != nil {
			if l.closed.Load() {
				return
			}
			r.errors.Add(1)
		}
	}
}
