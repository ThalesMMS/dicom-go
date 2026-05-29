//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package dimse

import (
	"os"
)

func openStorePathNoFollow(string) (*os.File, error) {
	return nil, ErrStoreInvalidSource
}

func openStoreRootedPathNoFollow(string, string) (*os.File, error) {
	return nil, ErrStoreInvalidSource
}
