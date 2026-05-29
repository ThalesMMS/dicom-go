//go:build windows

package dicomdir

import (
	"os"

	"golang.org/x/sys/windows"
)

func openedFileIdentity(file *os.File) (fileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{
		volume: uint64(info.VolumeSerialNumber),
		file:   uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}, nil
}
