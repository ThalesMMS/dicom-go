//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package dicomdir

import (
	"os"

	"golang.org/x/sys/unix"
)

func openedFileIdentity(file *os.File) (fileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{volume: uint64(stat.Dev), file: uint64(stat.Ino)}, nil
}
