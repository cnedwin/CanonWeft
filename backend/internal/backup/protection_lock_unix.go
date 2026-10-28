//go:build linux || darwin

package backup

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockProtection(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
