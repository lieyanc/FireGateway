//go:build linux

package proxy

import (
	"cmp"
	"errors"
	"io"
	"net"
	"syscall"
)

const (
	spliceMove     = 0x1
	spliceNonblock = 0x2
	fGetPipeSz     = 1032 // F_GETPIPE_SZ
	// pipeSize is the kernel default. Pipes are left at it rather than
	// enlarged: the kernel charges pipe capacity to the user even while the
	// pipe is empty, and once an unprivileged user passes
	// fs.pipe-user-pages-soft (64 MiB by default) new pipes shrink to two
	// pages. Larger pipes gain little throughput but reach that limit sooner.
	pipeSize = 64 << 10
)

type spipe struct {
	r, w int
	// shrunk is set when the pipe was created below the default capacity
	// (the user was over the pipe quota); such pipes are not pooled.
	shrunk bool
}

// pipePool reuses pipes across connections; a bounded channel rather than a
// sync.Pool because dropped pipes would leak their file descriptors.
var pipePool = make(chan *spipe, 64)

func getPipe() (*spipe, error) {
	select {
	case p := <-pipePool:
		return p, nil
	default:
	}
	var fds [2]int
	if err := syscall.Pipe2(fds[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		return nil, err
	}
	n, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fds[1]), fGetPipeSz, 0)
	return &spipe{r: fds[0], w: fds[1], shrunk: errno != 0 || int(n) < pipeSize}, nil
}

func putPipe(p *spipe, clean bool) {
	if clean && !p.shrunk {
		select {
		case pipePool <- p:
			return
		default:
		}
	}
	syscall.Close(p.r)
	syscall.Close(p.w)
}

// spliceCopy moves src to dst through a kernel pipe. Each readiness event is
// drained and pumped separately, so accounting (and rate limiting) happens
// per chunk while the data itself never touches user space. handled is false
// if splice is not supported for these sockets, in which case nothing was read.
func spliceCopy(c *Conn, dst, src *net.TCPConn, up bool) (handled bool, err error) {
	rc, err := src.SyscallConn()
	if err != nil {
		return false, nil
	}
	wc, err := dst.SyscallConn()
	if err != nil {
		return false, nil
	}
	p, err := getPipe()
	if err != nil {
		return false, nil
	}
	clean := true
	defer func() { putPipe(p, clean) }()

	first := true
	for {
		var n int64
		var serr error
		if err := rc.Read(func(fd uintptr) bool {
			for {
				n, serr = syscall.Splice(int(fd), nil, p.w, nil, c.chunk(), spliceMove|spliceNonblock)
				if serr != syscall.EINTR {
					return serr != syscall.EAGAIN
				}
			}
		}); err != nil {
			return true, err
		}
		if serr != nil {
			if first && errors.Is(serr, syscall.EINVAL) {
				return false, nil
			}
			return true, serr
		}
		first = false
		if n == 0 {
			return true, nil // EOF
		}
		for remain := n; remain > 0; {
			var m int64
			var werr error
			if err := wc.Write(func(fd uintptr) bool {
				for {
					m, werr = syscall.Splice(p.r, nil, int(fd), nil, int(remain), spliceMove|spliceNonblock)
					if werr != syscall.EINTR {
						return werr != syscall.EAGAIN
					}
				}
			}); err != nil {
				clean = false
				return true, err
			}
			if werr != nil || m == 0 {
				clean = false
				return true, cmp.Or(werr, io.ErrShortWrite)
			}
			remain -= m
		}
		if err := c.account(int(n), up); err != nil {
			return true, err
		}
	}
}
