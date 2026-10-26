//go:build windows

package webcleanup

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Chrome reads a per-user host from
// HKCU\Software\Google\Chrome\NativeMessagingHosts\<name>, whose default
// value is the host manifest's path.
const regBase = `Software\Google\Chrome\NativeMessagingHosts\`

func osRegistrations(name string) []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, regBase+name, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil || v == "" {
		return nil
	}
	return []string{v}
}

func osUnregister(name, _ string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, regBase+name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
