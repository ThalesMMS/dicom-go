//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package nofollow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func OpenFile(path string) (*os.File, error) {
	return openAbsolute(path, false)
}

func OpenDirectory(path string) (*os.File, error) {
	return openAbsolute(path, true)
}

func openAbsolute(path string, directory bool) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(filepath.Clean(absPath), string(filepath.Separator))
	if relative == "" || relative == "." {
		if !directory {
			return nil, errors.New("dicom nofollow: invalid transcode source")
		}
		fd, openErr := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, openErr
		}
		return fileFromFD(fd, absPath)
	}

	currentFD, err := openTraversalRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		if currentFD >= 0 {
			_ = unix.Close(currentFD)
		}
	}()

	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
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
	file, err := fileFromFD(currentFD, absPath)
	if err != nil {
		return nil, err
	}
	currentFD = -1
	return file, nil
}

func fileFromFD(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("dicom nofollow: create file from descriptor")
	}
	return file, nil
}

func CreateAt(parent *os.File, name string) (*os.File, error) {
	if parent == nil || !ValidName(name) {
		return nil, errors.New("dicom nofollow: invalid temporary entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return fileFromFD(fd, name)
}

func OpenAt(parent *os.File, name string) (*os.File, error) {
	if parent == nil || !ValidName(name) {
		return nil, errors.New("dicom nofollow: invalid directory entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return fileFromFD(fd, name)
}

func RemoveAt(parent *os.File, name string) error {
	if parent == nil || !ValidName(name) {
		return errors.New("dicom nofollow: invalid directory entry")
	}
	err := unix.Unlinkat(int(parent.Fd()), name, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
