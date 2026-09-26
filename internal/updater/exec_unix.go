//go:build !windows

package updater

import "syscall"

// replaceProcess swaps the current process image in place; the PID is kept,
// so supervisors such as systemd do not notice the restart.
func replaceProcess(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}
