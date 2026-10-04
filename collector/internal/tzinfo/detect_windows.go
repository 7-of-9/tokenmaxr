//go:build windows

package tzinfo

import (
	"syscall"
	"unsafe"
)

func detectPlatform() (Info, bool) {
	id := regString(syscall.HKEY_LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, "TimeZoneKeyName")
	region := regString(syscall.HKEY_CURRENT_USER, `Control Panel\International\Geo`, "Name")
	return fromWindows(id, region)
}

// regString reads a REG_SZ / REG_EXPAND_SZ value, returning "" on any error.
func regString(root syscall.Handle, path, name string) string {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	var h syscall.Handle
	if syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ, &h) != nil {
		return ""
	}
	defer syscall.RegCloseKey(h)

	var typ, size uint32
	if syscall.RegQueryValueEx(h, n, nil, &typ, nil, &size) != nil ||
		(typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ) || size < 2 || size > 4096 {
		return ""
	}
	buf := make([]uint16, size/2+1)
	if syscall.RegQueryValueEx(h, n, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
