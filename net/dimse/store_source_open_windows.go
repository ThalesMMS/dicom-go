//go:build windows

package dimse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openStorePathNoFollow(path string) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrStoreInvalidSource
	}
	volume := filepath.VolumeName(absPath)
	if volume == "" {
		return nil, ErrStoreInvalidSource
	}
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(filepath.Clean(absPath), root)
	if relative == "" || relative == "." {
		return nil, ErrStoreInvalidSource
	}
	return openStoreWindowsRelative(root, relative)
}

func openStoreRootedPathNoFollow(root, relative string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrStoreInvalidSource
	}
	return openStorePathNoFollow(filepath.Join(root, relative))
}

func openStoreWindowsRelative(root, relative string) (*os.File, error) {
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
	defer windows.CloseHandle(rootHandle)
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &rootInfo); err != nil || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, ErrStoreInvalidSource
	}
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
	var fileHandle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&fileHandle,
		windows.FILE_GENERIC_READ,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileHandle), filepath.Join(root, relative))
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, errors.New("dicom dimse: create source from handle")
	}
	return file, nil
}
