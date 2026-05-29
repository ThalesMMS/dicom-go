//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package pixeldata

import (
	"os"
)

func transcodePathSupported() bool      { return false }
func transcodeCanReplaceExisting() bool { return false }

func openTranscodeFileNoFollow(string) (*os.File, error) { return nil, ErrTranscodeUnsupported }

func openTranscodeDirectoryNoFollow(string) (*os.File, error) { return nil, ErrTranscodeUnsupported }

func createTranscodeFileAt(*os.File, string) (*os.File, error) { return nil, ErrTranscodeUnsupported }

func openTranscodeFileAt(*os.File, string) (*os.File, error) { return nil, ErrTranscodeUnsupported }

func removeTranscodeFileAt(*os.File, string) error { return ErrTranscodeUnsupported }

func replaceTranscodedFileAt(*os.File, *os.File, string, string, transcodeFileSnapshot) error {
	return ErrTranscodeUnsupported
}

func syncTranscodeDirectory(*os.File) error { return ErrTranscodeUnsupported }
