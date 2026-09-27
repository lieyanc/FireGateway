package proxy

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/lieyanc/FireGateway/internal/dnscache"
)

// minAttemptTimeout keeps each address a usable share of the dial timeout
// when a host has many addresses, as net.Dialer does.
const minAttemptTimeout = 2 * time.Second

// dialTCP connects to mapping idx of p, trying the target host's cached
// addresses in resolver order until one answers.
func dialTCP(ctx context.Context, p *policy, idx int) (*net.TCPConn, error) {
	ctx, cancel := context.WithTimeout(ctx, tcpDialTimeout)
	defer cancel()
	addrs, err := dnscache.Default.Lookup(ctx, p.host)
	if err != nil {
		return nil, err
	}
	port := uint16(p.ports[idx])
	var firstErr error
	for i, a := range addrs {
		actx := ctx
		if left := len(addrs) - i; left > 1 {
			dl, _ := ctx.Deadline()
			share := max(time.Until(dl)/time.Duration(left), minAttemptTimeout)
			var c context.CancelFunc
			actx, c = context.WithTimeout(ctx, share)
			defer c()
		}
		c, err := tcpDialer.DialContext(actx, "tcp", netip.AddrPortFrom(a, port).String())
		if err == nil {
			return c.(*net.TCPConn), nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}

// dialUDP returns a socket connected to mapping idx of p, using the first
// cached address of the target host.
func dialUDP(ctx context.Context, p *policy, idx int) (*net.UDPConn, error) {
	addrs, err := dnscache.Default.Lookup(ctx, p.host)
	if err != nil {
		return nil, err
	}
	return net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(addrs[0], uint16(p.ports[idx]))))
}
