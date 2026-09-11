//go:build windows

package pixeldata

import (
	"errors"
	"fmt"
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

func transcodePathSupported() bool      { return true }
func transcodeCanReplaceExisting() bool { return true }
func replaceTranscodedFileAt(parent, temporary *os.File, temporaryName, destinationName string, previous transcodeFileSnapshot) error {
	if parent == nil || temporary == nil || !validTranscodeEntryName(temporaryName) || !validTranscodeEntryName(destinationName) {
		return errors.New("dicom pixeldata: invalid publish entry")
	}
	return replaceTranscodedWindowsFileAt(parent, temporary, destinationName, previous, defaultTranscodeWindowsReplaceOperations())
}

type transcodeWindowsReplaceOperations struct {
	inspectTemporary   func(*os.File) (os.FileInfo, error)
	renameTemporary    func(parent, temporary *os.File, destinationName string, replace bool) error
	destinationMatches func(parent *os.File, destinationName string, expected os.FileInfo) bool
	closeTemporary     func(*os.File) error
}

func defaultTranscodeWindowsReplaceOperations() transcodeWindowsReplaceOperations {
	return transcodeWindowsReplaceOperations{
		inspectTemporary:   (*os.File).Stat,
		renameTemporary:    renameTranscodedWindowsFileAt,
		destinationMatches: sameTranscodeEntryAt,
		closeTemporary:     (*os.File).Close,
	}
}

func replaceTranscodedWindowsFileAt(parent, temporary *os.File, destinationName string, previous transcodeFileSnapshot, operations transcodeWindowsReplaceOperations) error {
	temporaryInfo, err := operations.inspectTemporary(temporary)
	if err != nil {
		return fmt.Errorf("dicom pixeldata: inspect publish temporary: %w", err)
	}
	if err := operations.renameTemporary(parent, temporary, destinationName, previous.exists); err != nil {
		return fmt.Errorf("dicom pixeldata: atomically publish destination: %w", err)
	}
	// Once the atomic rename succeeds, the new destination is authoritative.
	// Never roll it back to the previous file if verification or close fails.
	if !operations.destinationMatches(parent, destinationName, temporaryInfo) {
		return fmt.Errorf("dicom pixeldata: verify published destination: %w", ErrTranscodeDestinationUnsafe)
	}
	if err := operations.closeTemporary(temporary); err != nil {
		return fmt.Errorf("dicom pixeldata: close published destination: %w", err)
	}
	return nil
}

func renameTranscodedWindowsFileAt(parent, temporary *os.File, name string, replace bool) error {
	return nofollow.RenameAt(parent, temporary, name, replace)
}
func syncTranscodeDirectory(*os.File) error { return nil }
