//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pixeldata

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openTranscodeFileNoFollow(path string) (*os.File, error) {
	return openTranscodeAbsoluteNoFollow(path, false)
}

func openTranscodeDirectoryNoFollow(path string) (*os.File, error) {
	return openTranscodeAbsoluteNoFollow(path, true)
}

func openTranscodeAbsoluteNoFollow(path string, directory bool) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(filepath.Clean(absPath), string(filepath.Separator))
	if relative == "" || relative == "." {
		if !directory {
			return nil, errors.New("dicom pixeldata: invalid transcode source")
		}
		fd, openErr := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, openErr
		}
		return transcodeFileFromFD(fd, absPath)
	}

	currentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
	file, err := transcodeFileFromFD(currentFD, absPath)
	if err != nil {
		return nil, err
	}
	currentFD = -1
	return file, nil
}

func transcodeFileFromFD(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("dicom pixeldata: create file from descriptor")
	}
	return file, nil
}

func createTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	if parent == nil || !validTranscodeEntryName(name) {
		return nil, errors.New("dicom pixeldata: invalid temporary entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return transcodeFileFromFD(fd, name)
}

func openTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	if parent == nil || !validTranscodeEntryName(name) {
		return nil, errors.New("dicom pixeldata: invalid directory entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return transcodeFileFromFD(fd, name)
}

func removeTranscodeFileAt(parent *os.File, name string) error {
	if parent == nil || !validTranscodeEntryName(name) {
		return errors.New("dicom pixeldata: invalid directory entry")
	}
	err := unix.Unlinkat(int(parent.Fd()), name, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func replaceTranscodedFileAt(parent, temporary *os.File, temporaryName, destinationName string, previous transcodeFileSnapshot) error {
	if parent == nil || temporary == nil || !validTranscodeEntryName(temporaryName) || !validTranscodeEntryName(destinationName) {
		return errors.New("dicom pixeldata: invalid publish entry")
	}
	temporaryInfo, statErr := temporary.Stat()
	if statErr != nil {
		return statErr
	}
	renameErr := atomicReplaceTranscodeEntry(parent, temporaryName, destinationName, temporaryInfo, previous)
	closeErr := temporary.Close()
	if renameErr != nil {
		return renameErr
	}
	return closeErr
}

func syncTranscodeDirectory(parent *os.File) error {
	if parent == nil {
		return errors.New("dicom pixeldata: invalid destination directory")
	}
	return parent.Sync()
}

func validTranscodeEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
