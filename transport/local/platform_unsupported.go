//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package local

import "fmt"

func openPlatformStore(string) (blockStore, error) {
	return nil, fmt.Errorf("local CAS is unsupported on this operating system because safe descriptor-relative storage is unavailable")
}
