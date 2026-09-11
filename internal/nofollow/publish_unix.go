//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package nofollow

import (
	"os"

	"golang.org/x/sys/unix"
)

// PublishClosedAt links a completely written, synced and closed temporary into
// an absent destination. The caller must own mutation of the source entry.
// Existing destinations, including symlinks, are never replaced or followed.
func PublishClosedAt(parent *os.File, source, destination string, expected os.FileInfo) (bool, error) {
	if parent == nil || !ValidName(source) || !ValidName(destination) || expected == nil {
		return false, os.ErrInvalid
	}
	f, err := OpenAt(parent, source)
	if err != nil {
		return false, err
	}
	info, err := f.Stat()
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if !os.SameFile(info, expected) || !info.Mode().IsRegular() {
		return false, os.ErrInvalid
	}
	err = unix.Linkat(int(parent.Fd()), source, int(parent.Fd()), destination, 0)
	return err == nil, err
}
