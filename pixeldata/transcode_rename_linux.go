//go:build linux

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
		if err := unix.Renameat2(fd, temporaryName, fd, destinationName, unix.RENAME_NOREPLACE); err != nil {
			return ErrTranscodeDestinationUnsafe
		}
		if sameTranscodeEntryAt(parent, destinationName, temporaryInfo) {
			return nil
		}
		_ = unix.Renameat2(fd, destinationName, fd, temporaryName, unix.RENAME_NOREPLACE)
		return ErrTranscodeDestinationUnsafe
	}
	if err := unix.Renameat2(fd, temporaryName, fd, destinationName, unix.RENAME_EXCHANGE); err != nil {
		return ErrTranscodeDestinationUnsafe
	}
	if sameTranscodeEntryAt(parent, destinationName, temporaryInfo) && sameTranscodeEntryAt(parent, temporaryName, previous.info) {
		if err := unix.Unlinkat(fd, temporaryName, 0); err == nil {
			return nil
		}
	}
	_ = unix.Renameat2(fd, temporaryName, fd, destinationName, unix.RENAME_EXCHANGE)
	return ErrTranscodeDestinationUnsafe
}
