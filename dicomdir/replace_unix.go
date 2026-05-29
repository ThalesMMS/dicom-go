//go:build !windows

package dicomdir

import "os"

func replaceFileAtomically(source, destination string) error {
	return os.Rename(source, destination)
}

func syncFileSetDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
