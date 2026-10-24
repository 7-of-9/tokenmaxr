//go:build windows

package webcapture

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Chrome reads a per-user host from HKCU\Software\Google\Chrome\NativeMessagingHosts\<name>
// (default value: the host manifest's path).
const regBase = `Software\Google\Chrome\NativeMessagingHosts\`

func osRegister(name, manifest string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, regBase+name, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", manifest)
}

func osUnregister(name string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, regBase+name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// registered returns the manifest path Chrome would use ("" if none).
func osRegistered(name string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, regBase+name, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return v
}

const launcherName = "native-host.cmd"

func launcher(exe, home string) []byte {
	return []byte("@echo off\r\n\"" + exe + "\" --home \"" + home + "\" native-host %*\r\n")
}
