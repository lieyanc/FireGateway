package proxy

import "syscall"

// batchIO enables draining queued datagrams with recvmmsg.
const (
	batchIO     = true
	msgDontWait = syscall.MSG_DONTWAIT
)
