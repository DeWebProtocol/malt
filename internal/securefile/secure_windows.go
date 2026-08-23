//go:build windows

// Package securefile applies owner-only protection to local runtime state.
package securefile

import "golang.org/x/sys/windows"

const ownerSystemOnlyDACL = "D:P(A;;GA;;;SY)(A;;GA;;;OW)"
const ownerSystemDirectoryDACL = "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;OW)"

var canonicalOwnerSystemOnlyDACL = func() string {
	descriptor, err := windows.SecurityDescriptorFromString(ownerSystemOnlyDACL)
	if err != nil {
		return ""
	}
	return descriptor.String()
}()

var canonicalOwnerSystemDirectoryDACL = func() string {
	descriptor, err := windows.SecurityDescriptorFromString(ownerSystemDirectoryDACL)
	if err != nil {
		return ""
	}
	return descriptor.String()
}()

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

// IsSecureHandle verifies owner identity and the protected owner/SYSTEM-only
// DACL through an already opened non-replaceable handle. It never mutates the
// file and therefore remains safe on verified read paths.
func IsSecureHandle(handle windows.Handle) (bool, error) {
	return isSecureHandle(handle, canonicalOwnerSystemOnlyDACL)
}

// IsSecureDirectoryHandle verifies the protected inherited owner/SYSTEM-only
// directory DACL without changing it.
func IsSecureDirectoryHandle(handle windows.Handle) (bool, error) {
	return isSecureHandle(handle, canonicalOwnerSystemDirectoryDACL)
}

// IsCurrentOwnerHandle verifies that an already opened object belongs to the
// current process user.
func IsCurrentOwnerHandle(handle windows.Handle) (bool, error) {
	security, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return false, err
	}
	owner, _, err := security.Owner()
	if err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	if owner == nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return false, nil
	}
	return true, nil
}

func isSecureHandle(handle windows.Handle, canonicalDACL string) (bool, error) {
	owned, err := IsCurrentOwnerHandle(handle)
	if err != nil || !owned {
		return owned, err
	}
	daclOnly, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	if canonicalDACL == "" {
		return false, windows.ERROR_INVALID_SECURITY_DESCR
	}
	return daclOnly.String() == canonicalDACL, nil
}
