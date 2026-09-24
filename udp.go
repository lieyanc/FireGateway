package main

import (
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
)

type udpProxy struct {
	base
	conn   *net.UDPConn
	raddr  *net.UDPAddr
	mu     sync.RWMutex
	sess   map[netip.AddrPort]*udpSession
	closed atomic.Bool
}

// udpSession is a per-client connected socket towards the target, so replies
// can be routed back to the originating client.
type udpSession struct {
	up   *net.UDPConn
	last atomic.Int64 // unix nanos of last activity
}

func startUDP(r *Rule, idx int, m Mapping) (*udpProxy, error) {
	p := &udpProxy{base: newBase("udp", r, idx, m), sess: make(map[netip.AddrPort]*udpSession)}
	raddr, err := net.ResolveUDPAddr("udp", p.target)
	if err != nil {
		return nil, err
	}
	laddr, err := net.ResolveUDPAddr("udp", p.listen)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	conn.SetReadBuffer(udpSockBuf)
	conn.SetWriteBuffer(udpSockBuf)
	p.conn, p.raddr = conn, raddr
	p.log.Info("UDP proxy started")
	go p.serve()
	return p, nil
}

func (p *udpProxy) Close() error {
	p.closed.Store(true)
	err := p.conn.Close()
	p.mu.Lock()
	for k, s := range p.sess {
		s.up.Close()
		delete(p.sess, k)
	}
	p.mu.Unlock()
	return err
}

func (p *udpProxy) serve() {
	buf := make([]byte, udpBufSize)
	for {
		n, client, err := p.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if p.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			p.errors.Add(1)
			p.log.Debug("UDP read failed", "err", err)
			continue
		}
		p.forward(client, buf[:n])
	}
}

func (p *udpProxy) forward(client netip.AddrPort, pkt []byte) {
	// Retry once if the session was reaped between lookup and write.
	for range 2 {
		s, err := p.session(client)
		if err != nil {
			p.errors.Add(1)
			p.log.Warn("UDP dial target failed", "client", client.String(), "err", err)
			return
		}
		s.last.Store(time.Now().UnixNano())
		if _, err = s.up.Write(pkt); err == nil {
			p.messages.Add(1)
			p.bytesUp.Add(int64(len(pkt)))
			return
		}
		if !errors.Is(err, net.ErrClosed) {
			p.errors.Add(1)
			p.log.Debug("UDP forward failed", "client", client.String(), "err", err)
			return
		}
	}
}

func (p *udpProxy) session(client netip.AddrPort) (*udpSession, error) {
	p.mu.RLock()
	s := p.sess[client]
	p.mu.RUnlock()
	if s != nil {
		return s, nil
	}
	up, err := net.DialUDP("udp", nil, p.raddr)
	if err != nil {
		return nil, err
	}
	up.SetReadBuffer(udpSockBuf)
	s = &udpSession{up: up}
	s.last.Store(time.Now().UnixNano())
	// Only serve() creates sessions, so no double-insert race here.
	p.mu.Lock()
	p.sess[client] = s
	p.mu.Unlock()
	p.total.Add(1)
	p.active.Add(1)
	p.log.Debug("UDP session created", "client", client.String())
	go p.reply(client, s)
	return s, nil
}

func (p *udpProxy) reply(client netip.AddrPort, s *udpSession) {
	defer func() {
		s.up.Close()
		p.mu.Lock()
		if p.sess[client] == s {
			delete(p.sess, client)
		}
		p.mu.Unlock()
		p.active.Add(-1)
		p.log.Debug("UDP session closed", "client", client.String())
	}()

	buf := make([]byte, udpBufSize)
	// Deadline is refreshed only on expiry rather than per packet.
	s.up.SetReadDeadline(time.Now().Add(udpSessionTimeout))
	for {
		n, err := s.up.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				idleUntil := time.Unix(0, s.last.Load()).Add(udpSessionTimeout)
				if time.Now().Before(idleUntil) {
					s.up.SetReadDeadline(idleUntil)
					continue
				}
				return
			}
			if !errors.Is(err, net.ErrClosed) {
				// e.g. ECONNREFUSED from an ICMP port unreachable; keep the session.
				p.errors.Add(1)
				p.log.Debug("UDP upstream read failed", "client", client.String(), "err", err)
				continue
			}
			return
		}
		s.last.Store(time.Now().UnixNano())
		if _, err := p.conn.WriteToUDPAddrPort(buf[:n], client); err != nil {
			if p.closed.Load() {
				return
			}
			p.errors.Add(1)
			continue
		}
		p.bytesDown.Add(int64(n))
	}
}
