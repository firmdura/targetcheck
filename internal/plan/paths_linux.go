//go:build linux

// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// safeOpen walks from / with directory fds and O_NOFOLLOW at every component.
// O_NONBLOCK prevents a substituted FIFO/device from blocking before fstat can
// reject its file type. No path component is opened through a symlink.
func safeOpen(absolute string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(absolute) {
		return nil, fmt.Errorf("absolute path required")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(absolute), "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		if directory {
			return os.NewFile(uintptr(fd), "/"), nil
		}
		syscall.Close(fd)
		return nil, fmt.Errorf("ordinary file required")
	}
	for index, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if index < len(parts)-1 || directory {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), absolute)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, bareError(err)
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		f.Close()
		return nil, fmt.Errorf("unexpected declared path type")
	}
	return f, nil
}
