//go:build windows

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func open(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// syncDir is a no-op: NTFS renames are journaled and directories cannot be fsynced.
func syncDir(string) {}

// rename replaces to with POSIX semantics, so a reader that has the old file
// open (the app's status, `status` in a terminal: both open with delete
// sharing) keeps reading it and never fails the tick's save; plain
// MoveFileEx refuses to replace an open file. Where that is not supported
// (before Windows 10 1809, FAT, some network shares) it falls back to
// os.Rename, retried for about half a second while a reader lets go.
func rename(from, to string) error {
	err := posixRename(from, to)
	switch {
	case err == nil:
		return nil
	case transient(err), errors.Is(err, windows.ERROR_INVALID_PARAMETER), errors.Is(err, windows.ERROR_NOT_SUPPORTED), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return retryRename(from, to)
	}
	return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
}

func retryRename(from, to string) error {
	var err error
	for i := range 10 {
		if err = os.Rename(from, to); err == nil || !transient(err) {
			return err
		}
		time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
	}
	return err
}

func transient(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// fileRenameInfo is FILE_RENAME_INFO with the FileRenameInfoEx flags.
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func posixRename(from, to string) error {
	if !filepath.IsAbs(to) {
		abs, err := filepath.Abs(to)
		if err != nil {
			return err
		}
		to = abs
	}
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(src, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	size := unsafe.Offsetof(fileRenameInfo{}.FileName) + uintptr(len(name))*2
	buf := make([]uint64, (size+7)/8) // 8-byte aligned for the handle field
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32(len(name)-1) * 2 // bytes, without the NUL
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(&buf[0])), uint32(size))
}
