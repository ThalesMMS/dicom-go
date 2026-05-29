//go:build !windows

package netstore

import "os"

func protectInstanceFile(path string) error {
	return os.Chmod(path, 0o600)
}

func isPrivateInstanceFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().Perm() == 0o600, nil
}
