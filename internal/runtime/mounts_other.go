//go:build !linux

package runtime

import (
	"fmt"
	"runtime"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
)

func newPlatformMountAdapter() (filesystemmount.Adapter, error) {
	return nil, fmt.Errorf("%w: no read-only adapter for %s", ErrPlatformMountUnavailable, runtime.GOOS)
}
