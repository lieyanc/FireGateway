//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !windows

package ha

import (
	"errors"
	"os"
)

func lockJournal(string) (func(), error) {
	return nil, errors.New("peer HA requires OS file locking on this platform")
}

func replaceDurable(from, to string) error { return os.Rename(from, to) }
