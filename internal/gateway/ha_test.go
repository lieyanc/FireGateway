package gateway

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

func TestPreparedGateAndLocalServiceForwarding(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	m, store := newManager(t)
	m.clustered = true
	rule := tcpRule(freePort(t))
	rule.ID = "web"
	rule.TargetHost = "192.0.2.99"
	if _, err := m.Create(Admin, rule); err != nil {
		t.Fatal(err)
	}
	local, port := "127.0.0.1", upstream.Addr().(*net.TCPAddr).Port
	if err := m.UpdateNode(config.NodeConfig{RuleOverrides: map[config.RuleID]config.RuleOverride{"web": {TargetHost: &local, TargetPort: &port}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(); err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(rule.LocalPort))
	closed, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.SetDeadline(time.Now().Add(time.Second))
	_, _ = closed.Write([]byte("standby"))
	if n, _ := closed.Read(make([]byte, 16)); n != 0 {
		t.Fatal("standby forwarded traffic")
	}
	closed.Close()
	m.SetServing(true)
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte("hello"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("local service override not used: %q %v", buf, err)
	}
	if store.Get().Forward[0].TargetHost != "192.0.2.99" {
		t.Fatal("shared target was modified")
	}
	// Closing the gate refuses new connections and lets established ones finish.
	m.SetServing(false)
	_, _ = c.Write([]byte("again"))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "again" {
		t.Fatalf("closing the gate broke an established connection: %v", err)
	}
	late, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	_ = late.SetDeadline(time.Now().Add(time.Second))
	_, _ = late.Write([]byte("late"))
	if n, _ := late.Read(buf); n != 0 {
		t.Fatal("closed gate forwarded a new connection")
	}
}

func TestPrepareKeepsHealthyRulesWhenOneCannotBind(t *testing.T) {
	m, _ := newManager(t)
	m.clustered = true
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { busy.Close() })
	good, bad := tcpRule(freePort(t)), tcpRule(busy.Addr().(*net.TCPAddr).Port)
	good.ID, bad.ID = "good", "bad"
	good.LocalHost, bad.LocalHost = "127.0.0.1", "127.0.0.1"
	for _, r := range []config.Rule{good, bad} {
		if _, err := m.Create(Admin, r); err != nil {
			t.Fatal(err)
		}
	}
	err = m.Prepare()
	if err == nil || !strings.Contains(err.Error(), "rule bad") || strings.Contains(err.Error(), "rule good") {
		t.Fatalf("unexpected prepare result: %v", err)
	}
	if rn := m.Runner("good"); rn == nil || rn.Listeners() == 0 {
		t.Fatal("a failing rule tore down a healthy one")
	}
	busy.Close()
	if err = m.Prepare(); err != nil {
		t.Fatalf("prepare did not retry the freed port: %v", err)
	}
}
