//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

package pixeldata

import "os"

func transcodePathSupported() bool      { return false }
func transcodeCanReplaceExisting() bool { return false }

func atomicReplaceTranscodeEntry(*os.File, string, string, os.FileInfo, transcodeFileSnapshot) error {
	return ErrTranscodeUnsupported
}
