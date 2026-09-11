//go:build windows

package pixeldata_test

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertTranscodeDestinationPermissions(t *testing.T, path string, info os.FileInfo) {
	t.Helper()
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("destination %q is read-only", path)
	}

	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("destination DACL control = %#x, want SE_DACL_PROTECTED", control)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("destination DACL = %#v, want one owner-rights ACE", dacl)
	}

	ownerRights, err := windows.StringToSid("S-1-3-4")
	if err != nil {
		t.Fatal(err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags&windows.INHERITED_ACE != 0 || !transcodeHasFullFileAccess(ace.Mask) {
		t.Fatalf("destination ACE = %#v, want explicit full-access allow", ace)
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(ownerRights) {
		t.Fatalf("destination ACE SID = %s, want owner-rights SID %s", aceSID, ownerRights)
	}
}

func transcodeHasFullFileAccess(mask windows.ACCESS_MASK) bool {
	const fileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED |
		windows.SYNCHRONIZE | windows.ACCESS_MASK(0x1ff)
	return mask&windows.GENERIC_ALL != 0 || mask&fileAllAccess == fileAllAccess
}
