//go:build windows

package mount

import "golang.org/x/sys/windows"

// replaceFile uses the native replace-existing and write-through flags rather
// than os.Rename, whose replacement semantics are not guaranteed on Windows.
func replaceFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		from,
		to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
