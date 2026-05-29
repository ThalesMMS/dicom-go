//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package dimse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openStorePathNoFollow(path string) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrStoreInvalidSource
	}
	physicalParent, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return nil, err
	}
	absPath = filepath.Join(physicalParent, filepath.Base(absPath))
	return openStoreAbsolutePathNoFollow(absPath)
}

func openStoreAbsolutePathNoFollow(absPath string) (*os.File, error) {
	relative := strings.TrimPrefix(filepath.Clean(absPath), string(filepath.Separator))
	if relative == "" || relative == "." {
		return nil, ErrStoreInvalidSource
	}
	currentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(currentFD) }()
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 {
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
	file := os.NewFile(uintptr(currentFD), absPath)
	if file == nil {
		return nil, errors.New("dicom dimse: create source from descriptor")
	}
	currentFD = -1
	return file, nil
}

func openStoreRootedPathNoFollow(root, relative string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrStoreInvalidSource
	}
	return openStoreAbsolutePathNoFollow(filepath.Join(root, relative))
}
