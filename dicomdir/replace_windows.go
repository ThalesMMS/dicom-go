//go:build windows

package dicomdir

import "golang.org/x/sys/windows"

func replaceFileAtomically(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePath, destinationPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows MoveFileEx with MOVEFILE_WRITE_THROUGH provides the durability
// boundary for the replacement. Opening directories for Sync is not portable
// through os.File on Windows.
func syncFileSetDirectory(string) error { return nil }
