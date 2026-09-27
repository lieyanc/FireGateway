//go:build !linux

package proxy

// batchIO is off: x/net only batches on Linux, and elsewhere a non-blocking
// read flag is not portable.
const (
	batchIO     = false
	msgDontWait = 0
)
