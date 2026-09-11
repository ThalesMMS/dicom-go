//go:build windows

package nofollow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func OpenFile(path string) (*os.File, error) {
	return openWindows(path, false)
}

func OpenDirectory(path string) (*os.File, error) {
	return openWindows(path, true)
}

func openWindows(path string, directory bool) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absPath)
	if volume == "" {
		return nil, errors.New("dicom nofollow: invalid transcode path")
	}
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(filepath.Clean(absPath), root)
	if relative == "" || relative == "." {
		if !directory {
			return nil, errors.New("dicom nofollow: invalid transcode source")
		}
		return openWindowsRoot(root)
	}
	rootFile, err := openWindowsRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	return openWindowsRelative(rootFile, relative, directory, false, false)
}

func openWindowsRoot(root string) (*os.File, error) {
	rootName, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		rootName,
		windows.FILE_GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("dicom nofollow: invalid transcode root")
	}
	file := os.NewFile(uintptr(handle), root)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("dicom nofollow: create directory from handle")
	}
	return file, nil
}

func openWindowsRelative(parent *os.File, relative string, directory, create, deleteAccess bool) (*os.File, error) {
	if parent == nil || relative == "" {
		return nil, errors.New("dicom nofollow: invalid relative entry")
	}
	objectName, err := windows.NewNTUnicodeString(relative)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var securityDescriptor *windows.SECURITY_DESCRIPTOR
	if create {
		securityDescriptor, err = windows.SecurityDescriptorFromString("D:P(A;;GA;;;OW)")
		if err != nil {
			return nil, err
		}
		attributes.SecurityDescriptor = securityDescriptor
	}
	desiredAccess := uint32(windows.FILE_GENERIC_READ)
	disposition := uint32(windows.FILE_OPEN)
	if create {
		desiredAccess |= windows.FILE_GENERIC_WRITE
		desiredAccess |= windows.WRITE_DAC | windows.WRITE_OWNER
		disposition = windows.FILE_CREATE
	}
	if create || deleteAccess {
		desiredAccess |= windows.DELETE
	}
	createOptions := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		createOptions |= windows.FILE_DIRECTORY_FILE
	} else {
		createOptions |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		desiredAccess,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		createOptions,
		0,
		0,
	)
	if err != nil {
		return nil, normalizeWindowsError(err)
	}
	file := os.NewFile(uintptr(handle), relative)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("dicom nofollow: create file from handle")
	}
	return file, nil
}

func normalizeWindowsError(err error) error {
	var status windows.NTStatus
	if !errors.As(err, &status) {
		return err
	}
	switch status {
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return os.ErrExist
	case windows.STATUS_OBJECT_NAME_NOT_FOUND, windows.STATUS_OBJECT_PATH_NOT_FOUND:
		return os.ErrNotExist
	default:
		return err
	}
}

func CreateAt(parent *os.File, name string) (*os.File, error) {
	if !ValidName(name) {
		return nil, errors.New("dicom nofollow: invalid temporary entry")
	}
	return openWindowsRelative(parent, name, false, true, true)
}

func OpenAt(parent *os.File, name string) (*os.File, error) {
	if !ValidName(name) {
		return nil, errors.New("dicom nofollow: invalid directory entry")
	}
	return openWindowsRelative(parent, name, false, false, false)
}

func RemoveAt(parent *os.File, name string) error {
	if parent == nil || !ValidName(name) {
		return errors.New("dicom nofollow: invalid directory entry")
	}
	file, err := openWindowsRelative(parent, name, false, false, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	disposition := byte(1)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(file.Fd()), &status, &disposition, 1, windows.FileDispositionInformation)
}

func RenameAt(parent, temporary *os.File, destinationName string, replace bool) error {
	name, err := windows.UTF16FromString(destinationName)
	if err != nil {
		return err
	}
	if len(name)-1 > windows.MAX_LONG_PATH {
		return errors.New("dicom nofollow: destination name too long")
	}
	nameBytes := (len(name) - 1) * 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	}
	information.RootDirectory = windows.Handle(parent.Fd())
	information.FileNameLength = uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:nameBytes/2:nameBytes/2], name[:len(name)-1])
	var status windows.IO_STATUS_BLOCK
	return normalizeWindowsError(windows.NtSetInformationFile(windows.Handle(temporary.Fd()), &status, &buffer[0], uint32(bufferSize), windows.FileRenameInformation))
}

func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
