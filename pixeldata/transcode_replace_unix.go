//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pixeldata

import (
	"errors"
	"os"

	"github.com/ThalesMMS/dicom-go/internal/nofollow"
)

func openTranscodeFileNoFollow(path string) (*os.File, error) { return nofollow.OpenFile(path) }
func openTranscodeDirectoryNoFollow(path string) (*os.File, error) {
	return nofollow.OpenDirectory(path)
}
func createTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	return nofollow.CreateAt(parent, name)
}
func openTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	return nofollow.OpenAt(parent, name)
}
func removeTranscodeFileAt(parent *os.File, name string) error {
	return nofollow.RemoveAt(parent, name)
}
func validTranscodeEntryName(name string) bool { return nofollow.ValidName(name) }

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
