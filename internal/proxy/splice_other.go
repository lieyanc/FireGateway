//go:build !linux

package proxy

import "net"

func spliceCopy(*Conn, *net.TCPConn, *net.TCPConn, bool) (bool, error) { return false, nil }
