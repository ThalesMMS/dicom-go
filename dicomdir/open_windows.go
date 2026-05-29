//go:build windows

package dicomdir

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openRelativeFile(root, relative string) (*os.File, error) {
	return openRelative(root, relative, false)
}

func openRelativeDirectory(root, relative string) (*os.File, error) {
	return openRelative(root, relative, true)
}

func openRelative(root, relative string, directory bool) (*os.File, error) {
	rootName, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := windows.CreateFile(
		rootName,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &rootInfo); err != nil {
		_ = windows.CloseHandle(rootHandle)
		return nil, err
	}
	if rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(rootHandle)
		return nil, errors.New("dicomdir: invalid media root")
	}
	if relative == "" {
		file := os.NewFile(uintptr(rootHandle), root)
		if file == nil {
			_ = windows.CloseHandle(rootHandle)
			return nil, errors.New("dicomdir: create directory from handle")
		}
		return file, nil
	}
	defer windows.CloseHandle(rootHandle)

	objectName, err := windows.NewNTUnicodeString(relative)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: rootHandle,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var (
		fileHandle windows.Handle
		status     windows.IO_STATUS_BLOCK
	)
	createOptions := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		createOptions |= windows.FILE_DIRECTORY_FILE
	} else {
		createOptions |= windows.FILE_NON_DIRECTORY_FILE
	}
	err = windows.NtCreateFile(
		&fileHandle,
		windows.FILE_GENERIC_READ,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		createOptions,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileHandle), relative)
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, errors.New("dicomdir: create file from handle")
	}
	return file, nil
}
