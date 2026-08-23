//go:build windows

package local

import "golang.org/x/sys/windows"

const ownerSystemDirectoryDACL = "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;OW)"

func protectDirectory(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString(ownerSystemDirectoryDACL)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}
