//go:build stress

package proxy

import (
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/config"
)

// Local stress check only: sending the entire burst before reading replies
// depends on socket buffer limits and scheduling. Packet loss under load must
// not gate CI. Run explicitly with make stress-udp.
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
