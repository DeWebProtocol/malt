//go:build windows

package securefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureAppliesProtectedOwnerAndSystemDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Secure(path); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("file DACL inherits permissions instead of being protected")
	}
	sddl := descriptor.String()
	if !strings.Contains(sddl, ";;;SY)") || !strings.Contains(sddl, ";;;OW)") {
		t.Fatalf("file DACL = %q, want only SYSTEM and owner rights", sddl)
	}
}
