package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	udpSessionTimeout = 5 * time.Minute
	udpBufSize        = 64 * 1024
	udpSockBuf        = 4 << 20
	udpDialTimeout    = 5 * time.Second
	// udpBatch is how many queued datagrams one extra read drains, and so
	// how many can leave in one send.
	udpBatch = 16
	// udpDrainBackoff is how many reads skip draining after a drain found
	// nothing, so sparse traffic costs one syscall per datagram, not two.
	udpDrainBackoff = 8
)

// Traffic is read with one blocking read into a buffer the reader owns, then
// whatever else is already queued is drained without blocking into pooled
// buffers (recvmmsg on Linux) and sent on in one call per peer (sendmmsg).
// Pooling keeps an idle port or session at a single buffer while a busy one
// moves a batch per pair of syscalls.
type udpBufs struct{ msgs []ipv4.Message }

var udpBufPool = sync.Pool{New: func() any {
	b := &udpBufs{msgs: make([]ipv4.Message, udpBatch)}
	for i := range b.msgs {
		b.msgs[i].Buffers = [][]byte{make([]byte, udpBufSize)}
	}
	return b
}}

// drainer tracks whether draining is currently worthwhile for one reader.
type drainer struct{ skip int }

// drain reads datagrams already queued on pc without blocking. It returns
// nil when batching is unavailable or nothing was queued; a non-nil result
// must be handed back with putBufs.
func (d *drainer) drain(pc *ipv4.PacketConn) (*udpBufs, int) {
	if !batchIO {
		return nil, 0
	}
	if d.skip > 0 {
		d.skip--
		return nil, 0
	}
	b := udpBufPool.Get().(*udpBufs)
	n, _ := pc.ReadBatch(b.msgs, msgDontWait)
	if n <= 0 {
		udpBufPool.Put(b)
		d.skip = udpDrainBackoff
		return nil, 0
	}
	return b, n
}

func putBufs(b *udpBufs) {
	if b != nil {
		udpBufPool.Put(b)
	}
}

// writeAll sends ms in as few calls as the platform allows. It returns how
// many were sent; on error, ms[n] is the datagram that failed.
func writeAll(pc *ipv4.PacketConn, ms []ipv4.Message) (int, error) {
	sent := 0
	for sent < len(ms) {
		n, err := pc.WriteBatch(ms[sent:], 0)
		if n > 0 {
			sent += n
		}
		if err != nil {
			return sent, err
		}
		if n <= 0 {
			return sent, errors.New("udp: no progress writing batch")
		}
	}
	return sent, nil
}

type udpListener struct {
	rn     *Runner
	idx    int
	addr   string
	conn   *net.UDPConn
	pc     *ipv4.PacketConn // batch I/O on conn
	mu     sync.RWMutex
	sess   map[netip.AddrPort]*udpSession
	closed atomic.Bool
}

// udpSession is a per-client connected socket towards the target, so replies
// can be routed back to the originating client.
type udpSession struct {
	c      *Conn
	up     *net.UDPConn
	pc     *ipv4.PacketConn // batch I/O on up
	client *net.UDPAddr     // destination for batched replies
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
	l := &udpListener{rn: r, idx: idx, addr: addr, conn: conn, pc: ipv4.NewPacketConn(conn),
		sess: make(map[netip.AddrPort]*udpSession)}
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

type udpPacket struct {
	from netip.AddrPort
	data []byte
}

func (l *udpListener) serve() {
	buf := make([]byte, udpBufSize)
	pkts := make([]udpPacket, 0, udpBatch+1)
	out := make([]ipv4.Message, udpBatch+1)
	var d drainer
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
		pkts = append(pkts[:0], udpPacket{client, buf[:n]})
		b, k := d.drain(l.pc)
		for i := range k {
			m := &b.msgs[i]
			if a, ok := m.Addr.(*net.UDPAddr); ok {
				pkts = append(pkts, udpPacket{a.AddrPort(), m.Buffers[0][:m.N]})
			}
		}
		// Forward runs of datagrams from the same client together.
		for rest := pkts; len(rest) > 0; {
			j := 1
			for j < len(rest) && rest[j].from == rest[0].from {
				j++
			}
			l.forward(rest[0].from, rest[:j], out)
			rest = rest[j:]
		}
		putBufs(b)
	}
}

// forward sends datagrams from one client upstream.
func (l *udpListener) forward(client netip.AddrPort, pkts []udpPacket, out []ipv4.Message) {
	r := l.rn
	retried := false
	s := l.session(client)
	for s != nil && len(pkts) > 0 {
		n, err := s.send(pkts, out)
		if n > 0 {
			bytes := 0
			for _, p := range pkts[:n] {
				bytes += len(p.data)
			}
			r.messages.Add(int64(n))
			// Waiting for budget here stalls the read loop, so excess
			// datagrams are dropped by the kernel: policing, as UDP expects.
			s.c.account(bytes, true)
			pkts = pkts[n:]
		}
		switch {
		case err == nil:
		case errors.Is(err, net.ErrClosed) && !retried:
			// The session was reaped between lookup and write.
			retried = true
			s = l.session(client)
		case errors.Is(err, net.ErrClosed):
			return
		default:
			r.errors.Add(1)
			if r.debugOn() {
				r.log.Debug("UDP forward failed", "client", client.String(), "err", err)
			}
			pkts = pkts[1:] // drop the datagram that failed
		}
	}
}

// send writes datagrams to the target, returning how many were sent.
func (s *udpSession) send(pkts []udpPacket, out []ipv4.Message) (int, error) {
	if len(pkts) == 1 {
		if _, err := s.up.Write(pkts[0].data); err != nil {
			return 0, err
		}
		return 1, nil
	}
	ms := out[:len(pkts)]
	for i, p := range pkts {
		ms[i].Buffers = append(ms[i].Buffers[:0], p.data)
		ms[i].Addr = nil
	}
	return writeAll(s.pc, ms)
}

func (l *udpListener) session(client netip.AddrPort) *udpSession {
	l.mu.RLock()
	s := l.sess[client]
	l.mu.RUnlock()
	if s != nil {
		return s
	}
	r := l.rn
	p := r.policy()
	target := p.targets[l.idx]
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
	uc, err := dialUDP(ctx, p, l.idx)
	cancel()
	if err != nil {
		r.errors.Add(1)
		r.log.Warn("UDP dial target failed", "client", client.String(), "target", target, "err", err)
		r.release(c)
		return nil
	}
	s = &udpSession{c: c, up: uc, pc: ipv4.NewPacketConn(uc), client: net.UDPAddrFromAddrPort(client)}
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
	if r.debugOn() {
		r.log.Debug("UDP session created", "client", client.String(), "target", target,
			"remote", uc.RemoteAddr().String())
	}
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
		if r.debugOn() {
			r.log.Debug("UDP session closed", "client", client.String(),
				"bytesUp", s.c.up.Load(), "bytesDown", s.c.down.Load())
		}
	}()

	buf := make([]byte, udpBufSize)
	var out []ipv4.Message
	var d drainer
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
				if r.debugOn() {
					r.log.Debug("UDP upstream read failed", "client", client.String(), "err", err)
				}
				continue
			}
			return
		}
		b, k := d.drain(s.pc)
		bytes := n
		for i := range k {
			bytes += b.msgs[i].N
		}
		if s.c.account(bytes, false) != nil {
			putBufs(b)
			return
		}
		if k == 0 {
			_, err = l.conn.WriteToUDPAddrPort(buf[:n], client)
		} else {
			if out == nil {
				out = make([]ipv4.Message, udpBatch+1)
			}
			ms := out[:k+1]
			ms[0].Buffers = append(ms[0].Buffers[:0], buf[:n])
			for i := range k {
				ms[i+1].Buffers = append(ms[i+1].Buffers[:0], b.msgs[i].Buffers[0][:b.msgs[i].N])
			}
			for i := range ms {
				ms[i].Addr = s.client
			}
			_, err = writeAll(l.pc, ms)
		}
		putBufs(b)
		if err != nil {
			if l.closed.Load() {
				return
			}
			r.errors.Add(1)
		}
	}
}
