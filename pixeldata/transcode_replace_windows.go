//go:build windows

package pixeldata

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type transcodeFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func transcodePathSupported() bool      { return true }
func transcodeCanReplaceExisting() bool { return false }

func openTranscodeFileNoFollow(path string) (*os.File, error) {
	return openTranscodeWindowsNoFollow(path, false)
}

func openTranscodeDirectoryNoFollow(path string) (*os.File, error) {
	return openTranscodeWindowsNoFollow(path, true)
}

func openTranscodeWindowsNoFollow(path string, directory bool) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absPath)
	if volume == "" {
		return nil, errors.New("dicom pixeldata: invalid transcode path")
	}
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(filepath.Clean(absPath), root)
	if relative == "" || relative == "." {
		if !directory {
			return nil, errors.New("dicom pixeldata: invalid transcode source")
		}
		return openTranscodeWindowsRoot(root)
	}
	rootFile, err := openTranscodeWindowsRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	return openTranscodeWindowsRelative(rootFile, relative, directory, false, false)
}

func openTranscodeWindowsRoot(root string) (*os.File, error) {
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
		return nil, errors.New("dicom pixeldata: invalid transcode root")
	}
	file := os.NewFile(uintptr(handle), root)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("dicom pixeldata: create directory from handle")
	}
	return file, nil
}

func openTranscodeWindowsRelative(parent *os.File, relative string, directory, create, deleteAccess bool) (*os.File, error) {
	if parent == nil || relative == "" {
		return nil, errors.New("dicom pixeldata: invalid relative entry")
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
		return nil, err
	}
	file := os.NewFile(uintptr(handle), relative)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("dicom pixeldata: create file from handle")
	}
	return file, nil
}

func createTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	if !validTranscodeEntryName(name) {
		return nil, errors.New("dicom pixeldata: invalid temporary entry")
	}
	return openTranscodeWindowsRelative(parent, name, false, true, true)
}

func openTranscodeFileAt(parent *os.File, name string) (*os.File, error) {
	if !validTranscodeEntryName(name) {
		return nil, errors.New("dicom pixeldata: invalid directory entry")
	}
	return openTranscodeWindowsRelative(parent, name, false, false, false)
}

func removeTranscodeFileAt(parent *os.File, name string) error {
	if parent == nil || !validTranscodeEntryName(name) {
		return errors.New("dicom pixeldata: invalid directory entry")
	}
	file, err := openTranscodeWindowsRelative(parent, name, false, false, true)
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

func replaceTranscodedFileAt(parent, temporary *os.File, temporaryName, destinationName string, previous transcodeFileSnapshot) error {
	if parent == nil || temporary == nil || !validTranscodeEntryName(temporaryName) || !validTranscodeEntryName(destinationName) {
		return errors.New("dicom pixeldata: invalid publish entry")
	}
	if previous.exists {
		return ErrTranscodeDestinationUnsafe
	}
	name, err := windows.UTF16FromString(destinationName)
	if err != nil {
		return err
	}
	if len(name)-1 > windows.MAX_LONG_PATH {
		return errors.New("dicom pixeldata: destination name too long")
	}
	nameBytes := (len(name) - 1) * 2
	var layout transcodeFileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	information := (*transcodeFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.ReplaceIfExists = 0
	information.RootDirectory = windows.Handle(parent.Fd())
	information.FileNameLength = uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:nameBytes/2:nameBytes/2], name[:len(name)-1])
	var status windows.IO_STATUS_BLOCK
	renameErr := windows.NtSetInformationFile(windows.Handle(temporary.Fd()), &status, &buffer[0], uint32(bufferSize), windows.FileRenameInformation)
	closeErr := temporary.Close()
	if renameErr != nil {
		return renameErr
	}
	return closeErr
}

func syncTranscodeDirectory(*os.File) error { return nil }

func validTranscodeEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
