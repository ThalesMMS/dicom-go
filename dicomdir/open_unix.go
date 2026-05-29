//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package dicomdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openRelativeFile(root, relative string) (*os.File, error) {
	return openRelative(root, relative, false)
}

func openRelativeDirectory(root, relative string) (*os.File, error) {
	return openRelative(root, relative, true)
}

func openRelative(root, relative string, directory bool) (*os.File, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if relative == "" {
		file := os.NewFile(uintptr(rootFD), root)
		if file == nil {
			_ = unix.Close(rootFD)
			return nil, errors.New("dicomdir: create directory from descriptor")
		}
		return file, nil
	}
	currentFD := rootFD
	defer func() {
		_ = unix.Close(currentFD)
	}()

	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		nextFD, openErr := unix.Openat(currentFD, part, flags, 0)
		if openErr != nil {
			return nil, openErr
		}
		if closeErr := unix.Close(currentFD); closeErr != nil {
			_ = unix.Close(nextFD)
			return nil, closeErr
		}
		currentFD = nextFD
	}

	file := os.NewFile(uintptr(currentFD), filepath.Join(root, relative))
	if file == nil {
		return nil, errors.New("dicomdir: create file from descriptor")
	}
	currentFD = -1
	return file, nil
}
