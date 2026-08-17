//go:build linux

package fusefs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type mountIdentity struct {
	ID         uint64
	Mountpoint string
	Filesystem string
	Source     string
}

func readMountIdentity(mountpoint string) (*mountIdentity, error) {
	fd, err := unix.Open(mountpoint, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open mountpoint: %w", err)
	}
	defer unix.Close(fd)
	fdinfo, err := os.Open(filepath.Join("/proc/self/fdinfo", strconv.Itoa(fd)))
	if err != nil {
		return nil, fmt.Errorf("open mountpoint fdinfo: %w", err)
	}
	mountID, err := parseVisibleMountID(fdinfo)
	closeErr := fdinfo.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close mountpoint fdinfo: %w", closeErr)
	}
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("open mount table: %w", err)
	}
	defer file.Close()
	return parseMountIdentity(file, mountpoint, mountID)
}

func parseVisibleMountID(reader io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found || strings.TrimSpace(key) != "mnt_id" {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || id == 0 {
			return 0, fmt.Errorf("mountpoint fdinfo contains an invalid mount ID")
		}
		return id, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read mountpoint fdinfo: %w", err)
	}
	return 0, fmt.Errorf("mountpoint fdinfo does not contain a mount ID")
}

func parseMountIdentity(reader io.Reader, mountpoint string, visibleMountID uint64) (*mountIdentity, error) {
	if visibleMountID == 0 {
		return nil, fmt.Errorf("visible mount ID must be nonzero")
	}
	want := filepath.Clean(mountpoint)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if len(fields) < 7 || separator < 6 || separator+2 >= len(fields) {
			return nil, fmt.Errorf("mount table contains a malformed record")
		}
		decodedMountpoint, err := unescapeMountField(fields[4])
		if err != nil {
			return nil, err
		}
		if filepath.Clean(decodedMountpoint) != want {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("mount table contains an invalid mount ID")
		}
		if id != visibleMountID {
			continue
		}
		source, err := unescapeMountField(fields[separator+2])
		if err != nil {
			return nil, err
		}
		return &mountIdentity{
			ID: id, Mountpoint: decodedMountpoint,
			Filesystem: fields[separator+1], Source: source,
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read mount table: %w", err)
	}
	return nil, nil
}

func unescapeMountField(value string) (string, error) {
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) {
			return "", fmt.Errorf("mount table contains an incomplete escape")
		}
		escaped := value[index+1 : index+4]
		switch escaped {
		case "040":
			result.WriteByte(' ')
		case "011":
			result.WriteByte('\t')
		case "012":
			result.WriteByte('\n')
		case "134":
			result.WriteByte('\\')
		default:
			return "", fmt.Errorf("mount table contains unsupported escape \\%s", escaped)
		}
		index += 3
	}
	return result.String(), nil
}
