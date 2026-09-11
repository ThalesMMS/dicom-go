//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package nofollow

import (
	"errors"
	"os"
	"path/filepath"
)

var errUnsupported = errors.New("dicom nofollow: platform unsupported")

func OpenFile(string) (*os.File, error)           { return nil, errUnsupported }
func OpenDirectory(string) (*os.File, error)      { return nil, errUnsupported }
func CreateAt(*os.File, string) (*os.File, error) { return nil, errUnsupported }
func OpenAt(*os.File, string) (*os.File, error)   { return nil, errUnsupported }
func RemoveAt(*os.File, string) error             { return errUnsupported }
func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
func PublishClosedAt(*os.File, string, string, os.FileInfo) (bool, error) {
	return false, errUnsupported
}
