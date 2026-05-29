//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package dicomdir

import (
	"errors"
	"os"
)

func openedFileIdentity(*os.File) (fileIdentity, error) {
	return fileIdentity{}, errors.New("dicomdir: file identity is unsupported on this platform")
}
