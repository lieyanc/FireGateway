//go:build windows

package updater

import "os"

// replaceProcess starts a fresh copy of the binary and exits, since Windows
// has no exec(2) equivalent.
func replaceProcess(path string, args, env []string) error {
	proc, err := os.StartProcess(path, args, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Env:   env,
	})
	if err != nil {
		return err
	}
	_ = proc.Release()
	os.Exit(0)
	return nil
}
