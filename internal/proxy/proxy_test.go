package proxy

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
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
