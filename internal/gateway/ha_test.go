package gateway

import (
	"io"
	"net"
	"strconv"
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
	m.Demote()
	rule := tcpRule(freePort(t))
	rule.ID = "web"
	rule.TargetHost = "192.0.2.99"
	if _, err := m.Create(rule); err != nil {
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
	if !m.Activate(time.Now().Add(time.Minute), store.Rules().Snapshot().Checksum) {
		t.Fatal("could not activate prepared revision")
	}
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
	m.Demote()
	if n, err := c.Read(buf); n != 0 || err == nil {
		t.Fatal("demotion left old connection alive")
	}
}
