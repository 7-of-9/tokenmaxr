//go:build windows

// Package userpath puts <home>/bin on the user's PATH: HKCU\Environment\Path
// on Windows, a ~/.local/bin symlink on macOS.
package userpath

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

func same(a, b string) bool {
	clean := func(s string) string {
		return strings.ToLower(strings.TrimRight(filepath.Clean(strings.TrimSpace(s)), `\`))
	}
	return a != "" && clean(a) == clean(b)
}

func read() (string, uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", registry.EXPAND_SZ, err
	}
	defer k.Close()
	v, typ, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return "", registry.EXPAND_SZ, nil
	}
	return v, typ, err
}

func write(v string, typ uint32) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if typ == registry.SZ {
		err = k.SetStringValue("Path", v)
	} else {
		err = k.SetExpandStringValue("Path", v)
	}
	if err == nil {
		broadcast()
	}
	return err
}

// broadcast tells Explorer (and new shells) that the environment changed.
func broadcast() {
	env, _ := windows.UTF16PtrFromString("Environment")
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xFFFF, 0x001A, 0x0002
	var result uintptr
	proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}

// Contains reports whether dir is on the user PATH.
func Contains(dir string) bool {
	v, _, err := read()
	if err != nil {
		return false
	}
	for _, p := range strings.Split(v, ";") {
		if same(p, dir) {
			return true
		}
	}
	return false
}

// Add appends dir to the user PATH if absent. hint is printed to the user.
func Add(dir string) (hint string, err error) {
	v, typ, err := read()
	if err != nil {
		return "", err
	}
	for _, p := range strings.Split(v, ";") {
		if same(p, dir) {
			return "", nil
		}
	}
	if v != "" && !strings.HasSuffix(v, ";") {
		v += ";"
	}
	if err := write(v+dir, typ); err != nil {
		return "", err
	}
	return "added " + dir + " to your user PATH (open a new terminal to use " + buildinfo.Product + ")", nil
}

// Remove drops dir from the user PATH.
func Remove(dir string) error {
	v, typ, err := read()
	if err != nil || v == "" {
		return err
	}
	var keep []string
	removed := false
	for _, p := range strings.Split(v, ";") {
		if same(p, dir) {
			removed = true
			continue
		}
		if p != "" {
			keep = append(keep, p)
		}
	}
	if !removed {
		return nil
	}
	return write(strings.Join(keep, ";"), typ)
}
