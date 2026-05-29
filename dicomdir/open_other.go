//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package dicomdir

import (
	"errors"
	"os"
)

func openRelativeFile(_, _ string) (*os.File, error) {
	return nil, errors.New("dicomdir: secure referenced-file opening is unsupported on this platform")
}
