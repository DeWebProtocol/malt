//go:build windows

// Package securefile applies owner-only protection to local runtime state.
package securefile

import "golang.org/x/sys/windows"

const ownerSystemOnlyDACL = "D:P(A;;GA;;;SY)(A;;GA;;;OW)"

func Secure(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString(ownerSystemOnlyDACL)
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
