//go:build linux

package runtime

import (
	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	fusefs "github.com/dewebprotocol/malt-client/filesystem/platform/fuse"
)

func newPlatformMountAdapter() (filesystemmount.Adapter, error) { return fusefs.New(), nil }
