//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package main

import "os"

func lockArchiveFile(*os.File) error { return errArchive }
