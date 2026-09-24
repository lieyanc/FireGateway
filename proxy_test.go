package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLoadLegacyConfig(t *testing.T) {
	for _, f := range []string{"config.example.json", "config-test-local.json"} {
		c, err := loadConfig(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, r := range c.Forward {
			if _, err := r.Mappings(); err != nil {
				t.Errorf("%s rule %s: %v", f, r.ID, err)
			}
		}
	}
	c, _ := loadConfig("config.example.json")
	if m, _ := c.Forward[2].Mappings(); len(m) != 11 || m[10] != (Mapping{4010, 5010}) {
		t.Errorf("unexpected range expansion: %v", m)
	}
}

func TestInvalidRange(t *testing.T) {
	r := Rule{LocalPortRange: []int{1000, 1005}, TargetPortRange: []int{2000, 2004}}
	if _, err := r.Mappings(); err == nil {
		t.Fatal("expected mismatched range error")
	}
}

func TestMissingForward(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"api":{}}`), 0o644)
	if _, err := loadConfig(p); err == nil {
		t.Fatal("expected error")
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestTCPForward(t *testing.T) {
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	lp := freePort(t)
	r := &Rule{ID: "t", Type: "tcp", LocalHost: "127.0.0.1", LocalPort: lp,
		TargetHost: "127.0.0.1", TargetPort: echo.Addr().(*net.TCPAddr).Port}
	ps := startRule(r)
	if len(ps) != 1 {
		t.Fatal("proxy not started")
	}
	defer ps[0].Close()

	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(lp))
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

	time.Sleep(50 * time.Millisecond)
	s := ps[0].Stats()
	if s.TotalConnections != 1 || s.ActiveConnections != 0 || s.BytesUpstream != int64(len(payload)) {
		t.Errorf("unexpected stats: %+v", s)
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
	r := &Rule{ID: "u", Type: "udp", LocalHost: "127.0.0.1", LocalPortRange: []int{lp, lp},
		TargetHost: "127.0.0.1", TargetPortRange: []int{echo.LocalAddr().(*net.UDPAddr).Port, echo.LocalAddr().(*net.UDPAddr).Port}}
	ps := startRule(r)
	if len(ps) != 1 {
		t.Fatal("proxy not started")
	}
	defer ps[0].Close()

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
	if s := ps[0].Stats(); s.TotalConnections != 3 || s.MessagesForwarded != 3 {
		t.Errorf("unexpected stats: %+v", s)
	}
}
