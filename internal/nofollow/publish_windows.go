//go:build windows

package nofollow

import "os"

// PublishClosedAt reopens a completely written, synced and closed temporary,
// verifies its identity, then renames it by handle without replacing a target.
func PublishClosedAt(parent *os.File, source, destination string, expected os.FileInfo) (bool, error) {
	if parent == nil || !ValidName(source) || !ValidName(destination) || expected == nil {
		return false, os.ErrInvalid
	}
	f, err := openWindowsRelative(parent, source, false, false, true)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, expected) || !info.Mode().IsRegular() {
		return false, os.ErrInvalid
	}
	if err := RenameAt(parent, f, destination, false); err != nil {
		return false, err
	}
	// The rename is authoritative even if closing its read handle fails.
	return true, f.Close()
}
