//go:build darwin

package pixeldata

import (
	"os"

	"golang.org/x/sys/unix"
)

func transcodePathSupported() bool      { return true }
func transcodeCanReplaceExisting() bool { return true }

func atomicReplaceTranscodeEntry(parent *os.File, temporaryName, destinationName string, temporaryInfo os.FileInfo, previous transcodeFileSnapshot) error {
	fd := int(parent.Fd())
	if !previous.exists {
		if err := unix.RenameatxNp(fd, temporaryName, fd, destinationName, unix.RENAME_EXCL); err != nil {
			return ErrTranscodeDestinationUnsafe
		}
		if sameTranscodeEntryAt(parent, destinationName, temporaryInfo) {
			return nil
		}
		_ = unix.RenameatxNp(fd, destinationName, fd, temporaryName, unix.RENAME_EXCL)
		return ErrTranscodeDestinationUnsafe
	}
	if err := unix.RenameatxNp(fd, temporaryName, fd, destinationName, unix.RENAME_SWAP); err != nil {
		return ErrTranscodeDestinationUnsafe
	}
	if sameTranscodeEntryAt(parent, destinationName, temporaryInfo) && sameTranscodeEntryAt(parent, temporaryName, previous.info) {
		if err := unix.Unlinkat(fd, temporaryName, 0); err == nil {
			return nil
		}
	}
	_ = unix.RenameatxNp(fd, temporaryName, fd, destinationName, unix.RENAME_SWAP)
	return ErrTranscodeDestinationUnsafe
}
