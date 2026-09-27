package proxy

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
	"github.com/lieyanc/FireGateway/internal/dnscache"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// tcpEcho starts an echo server and returns its port.
func tcpEcho(t *testing.T) int {
	t.Helper()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return echo.Addr().(*net.TCPAddr).Port
}

func startTCPRule(t *testing.T, mod func(*config.Rule)) (*Runner, string) {
	t.Helper()
	lp := freePort(t)
	r := &config.Rule{ID: "t", Type: "tcp", Status: "active", LocalHost: "127.0.0.1", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: tcpEcho(t)}
	if mod != nil {
		mod(r)
	}
	rn, err := Start(r)
	if err != nil || rn.Listeners() != 1 {
		t.Fatalf("start: err=%v listeners=%d failures=%v", err, rn.Listeners(), rn.Failures())
	}
	t.Cleanup(rn.Stop)
	return rn, "127.0.0.1:" + strconv.Itoa(lp)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTCPForward(t *testing.T) {
	rn, addr := startTCPRule(t, nil)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4<<20)
	rand.Read(payload)
	go func() { c.Write(payload); c.(*net.TCPConn).CloseWrite() }()
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: err=%v len=%d", err, len(got))
	}
	c.Close()

	waitFor(t, "connection release", func() bool { return rn.Snapshot().Active == 0 })
	s := rn.Snapshot()
	if s.Total != 1 || s.BytesUp != int64(len(payload)) || s.BytesDn != int64(len(payload)) {
		t.Errorf("unexpected stats: %+v", s)
	}
}

// Counters must advance while a connection is still open, not only at close.
func TestTCPLiveCounters(t *testing.T) {
	rn, addr := startTCPRule(t, nil)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	buf := make([]byte, 5)
	io.ReadFull(c, buf)
	waitFor(t, "live byte counters", func() bool {
		s := rn.Snapshot()
		return s.BytesUp == 5 && s.BytesDn == 5
	})
	conns := rn.Conns()
	if len(conns) != 1 || conns[0].BytesUp != 5 {
		t.Fatalf("unexpected conns: %+v", conns)
	}
	if !rn.CloseConn(conns[0].ID) {
		t.Fatal("CloseConn returned false")
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("expected closed connection")
	}
	waitFor(t, "connection release", func() bool { return rn.Snapshot().Active == 0 })
}

func expectRefused(t *testing.T, addr string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("x"))
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatalf("expected refused connection, read %d bytes", n)
	}
}

func TestTCPACL(t *testing.T) {
	rn, addr := startTCPRule(t, func(r *config.Rule) {
		r.ACL = &config.ACL{Mode: "deny", CIDRs: []string{"127.0.0.0/8"}}
	})
	expectRefused(t, addr)
	if s := rn.Snapshot(); s.Rejected != 1 || s.Total != 0 {
		t.Errorf("unexpected stats: %+v", s)
	}

	// Switching to an allow-list hot-applies without re-binding.
	rule := &config.Rule{ID: "t", Type: "tcp", Status: "active", LocalHost: "127.0.0.1", LocalPort: 1,
		TargetHost: "127.0.0.1", TargetPort: 1, ACL: &config.ACL{Mode: "allow", CIDRs: []string{"127.0.0.1"}}}
	pol := rn.policy()
	rule.TargetPort, _ = strconv.Atoi(pol.targets[0][len("127.0.0.1:"):])
	if err := rn.Apply(rule); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ok"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
		t.Fatalf("allowed client failed: %v", err)
	}

	// Denying the client again closes its live connection.
	rule.ACL = &config.ACL{Mode: "allow", CIDRs: []string{"10.0.0.0/8"}}
	rn.Apply(rule)
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected connection closed by ACL update")
	}
}

func TestTCPMaxConnections(t *testing.T) {
	rn, addr := startTCPRule(t, func(r *config.Rule) {
		r.Limits = &config.Limits{MaxConnections: 1}
	})
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	waitFor(t, "first connection", func() bool { return rn.Snapshot().Active == 1 })
	expectRefused(t, addr)
	if s := rn.Snapshot(); s.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %+v", s)
	}
}

func TestTCPBandwidthLimit(t *testing.T) {
	const bw = 256 << 10 // 256 KiB/s
	_, addr := startTCPRule(t, func(r *config.Rule) {
		r.Limits = &config.Limits{Bandwidth: bw}
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Burst is 64 KiB, so 64 KiB + 128 KiB takes at least ~0.5s.
	payload := make([]byte, 192<<10)
	start := time.Now()
	go c.Write(payload)
	if _, err := io.ReadFull(c, make([]byte, len(payload))); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Errorf("transfer took %v, bandwidth limit not applied", d)
	}
}

func TestPortRangePartialFailure(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	p := busy.Addr().(*net.TCPAddr).Port
	// Range [p-1, p]: p is taken, p-1 is very likely free.
	r := &config.Rule{ID: "r", Type: "tcp", Status: "active", LocalHost: "127.0.0.1",
		LocalPortRange: []int{p - 1, p}, TargetHost: "127.0.0.1", TargetPortRange: []int{1000, 1001}}
	rn, err := Start(r)
	if err != nil {
		t.Fatal(err)
	}
	defer rn.Stop()
	if len(rn.Failures()) != 1 || rn.Failures()[0].Port != p {
		t.Fatalf("expected failure on port %d, got %+v", p, rn.Failures())
	}
}

func TestUDPForward(t *testing.T) {
	echo, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer echo.Close()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := echo.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			echo.WriteToUDPAddrPort(buf[:n], a)
		}
	}()

	lp := freePort(t)
	ep := echo.LocalAddr().(*net.UDPAddr).Port
	r := &config.Rule{ID: "u", Type: "udp", Status: "active", LocalHost: "127.0.0.1", LocalPortRange: []int{lp, lp},
		TargetHost: "127.0.0.1", TargetPortRange: []int{ep, ep}}
	rn, err := Start(r)
	if err != nil || rn.Listeners() != 1 {
		t.Fatalf("start: %v %v", err, rn.Failures())
	}
	defer rn.Stop()

	for i := range 3 {
		c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(lp))
		if err != nil {
			t.Fatal(err)
		}
		msg := []byte("hello-" + strconv.Itoa(i))
		c.Write(msg)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 100)
		n, err := c.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], msg) {
			t.Fatalf("client %d: err=%v got=%q", i, err, buf[:n])
		}
		c.Close()
	}
	if s := rn.Snapshot(); s.Total != 3 || s.Messages != 3 || s.Active != 3 {
		t.Errorf("unexpected stats: %+v", s)
	}
	if n := rn.CloseAll(); n != 3 {
		t.Errorf("CloseAll closed %d sessions, want 3", n)
	}
	waitFor(t, "session release", func() bool { return rn.Snapshot().Active == 0 })
}

// BenchmarkTCPThroughput streams through a sink target to measure forwarding
// throughput (splice path on Linux).
func BenchmarkTCPThroughput(b *testing.B) {
	sink, _ := net.Listen("tcp", "127.0.0.1:0")
	defer sink.Close()
	go func() {
		for {
			c, err := sink.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	lp := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rn, err := Start(&config.Rule{ID: "b", Type: "tcp", Status: "active", LocalHost: "127.0.0.1", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: sink.Addr().(*net.TCPAddr).Port})
	if err != nil {
		b.Fatal(err)
	}
	defer rn.Stop()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(lp))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 1<<20)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for range b.N {
		if _, err := c.Write(buf); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUDPEcho pushes 1200-byte datagrams (WireGuard/game sized) through
// the forwarder to an echo target with a bounded in-flight window, measuring
// round-trip packet throughput. Lost datagrams reopen the window on timeout.
func BenchmarkUDPEcho(b *testing.B) {
	echo, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer echo.Close()
	echo.SetReadBuffer(4 << 20)
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := echo.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			echo.WriteToUDPAddrPort(buf[:n], a)
		}
	}()
	lp := freeUDPPort(b)
	rn, err := Start(&config.Rule{ID: "b", Type: "udp", Status: "active", LocalHost: "127.0.0.1", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: echo.LocalAddr().(*net.UDPAddr).Port})
	if err != nil {
		b.Fatal(err)
	}
	defer rn.Stop()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lp})
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	c.SetReadBuffer(4 << 20)

	const window = 128
	pkt := make([]byte, 1200)
	credit := make(chan struct{}, window)
	for range window {
		credit <- struct{}{}
	}
	done := make(chan struct{})
	lost := 0
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for got := 0; got+lost < b.N; {
			c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			if _, err := c.Read(buf); err != nil {
				// Treat whatever is still in flight as lost.
				inflight := window - len(credit)
				lost += inflight
				for range inflight {
					credit <- struct{}{}
				}
				continue
			}
			got++
			credit <- struct{}{}
		}
	}()
	b.SetBytes(int64(len(pkt)))
	b.ResetTimer()
	for range b.N {
		<-credit
		c.Write(pkt)
	}
	<-done
	b.ReportMetric(float64(lost), "lost")
}

func freeUDPPort(tb testing.TB) int {
	tb.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func udpEcho(t *testing.T, network string, ip net.IP) int {
	t.Helper()
	echo, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		t.Skip("cannot listen:", err)
	}
	t.Cleanup(func() { echo.Close() })
	echo.SetReadBuffer(8 << 20)
	echo.SetWriteBuffer(8 << 20)
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := echo.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			echo.WriteToUDPAddrPort(buf[:n], a)
		}
	}()
	return echo.LocalAddr().(*net.UDPAddr).Port
}

// A burst exercises the batched drain and send paths in both directions,
// including datagrams near the maximum size.
func TestUDPBurst(t *testing.T) {
	ep := udpEcho(t, "udp4", net.IPv4(127, 0, 0, 1))
	lp := freeUDPPort(t)
	rn, err := Start(&config.Rule{ID: "u", Type: "udp", Status: "active", LocalHost: "127.0.0.1", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: ep})
	if err != nil || rn.Listeners() != 1 {
		t.Fatalf("start: %v %v", err, rn.Failures())
	}
	defer rn.Stop()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lp})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadBuffer(8 << 20)

	sizes := []int{1, 1200, 60000, 512, 65507, 9000}
	want := map[string]int{}
	total := 0
	const rounds = 20
	for i := range rounds * len(sizes) {
		p := make([]byte, sizes[i%len(sizes)])
		rand.Read(p)
		want[string(p)]++
		total += len(p)
		if _, err := c.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 65536)
	for range rounds * len(sizes) {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (%d datagrams missing)", err, len(want))
		}
		k := string(buf[:n])
		if want[k] == 0 {
			t.Fatalf("unexpected or corrupted datagram of %d bytes", n)
		}
		if want[k]--; want[k] == 0 {
			delete(want, k)
		}
	}
	waitFor(t, "counters", func() bool {
		s := rn.Snapshot()
		return s.Messages == int64(rounds*len(sizes)) && s.BytesUp == int64(total) && s.BytesDn == int64(total)
	})
}

// A dual-stack listener must answer IPv4 clients (v4-mapped on the socket)
// on the batched reply path too.
func TestUDPDualStack(t *testing.T) {
	ep := udpEcho(t, "udp4", net.IPv4(127, 0, 0, 1))
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6unspecified})
	if err != nil {
		t.Skip("no IPv6:", err)
	}
	lp := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	rn, err := Start(&config.Rule{ID: "u", Type: "udp", Status: "active", LocalHost: "::", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: ep})
	if err != nil || rn.Listeners() != 1 {
		t.Fatalf("start: %v %v", err, rn.Failures())
	}
	defer rn.Stop()
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lp})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	const n = 200
	for i := range n {
		c.Write([]byte(strconv.Itoa(i)))
	}
	buf := make([]byte, 64)
	for i := range n {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(buf); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
}

// Hostname targets resolve through the DNS cache; "localhost" may list ::1
// first, which the TCP dialer must skip past when nothing listens there.
func TestTCPHostnameTarget(t *testing.T) {
	_, addr := startTCPRule(t, func(r *config.Rule) { r.TargetHost = "localhost" })
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo via hostname: %q, %v", buf, err)
	}
}

// Hot-swapping the target host moves the rule's DNS cache reference.
func TestApplyMovesDNSReference(t *testing.T) {
	cached := func(host string) bool {
		return slices.ContainsFunc(dnscache.Default.Entries(), func(e dnscache.Entry) bool { return e.Host == host })
	}
	rn, _ := startTCPRule(t, func(r *config.Rule) { r.TargetHost = "localhost" })
	if !cached("localhost") {
		t.Fatal("hostname target not cached after start")
	}
	rule := &config.Rule{ID: "t", Type: "tcp", Status: "active", LocalHost: "127.0.0.1", LocalPort: 1,
		TargetHost: "127.0.0.1", TargetPort: 1}
	if err := rn.Apply(rule); err != nil {
		t.Fatal(err)
	}
	if cached("localhost") {
		t.Fatal("old hostname still cached after switching to an IP target")
	}
	rule.TargetHost = "localhost"
	rn.Apply(rule)
	rn.Stop()
	if cached("localhost") {
		t.Fatal("hostname still cached after stop")
	}
}
