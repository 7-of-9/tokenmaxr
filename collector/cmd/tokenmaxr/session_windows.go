//go:build windows

package main

import "golang.org/x/sys/windows"

// desktopSession: any interactive session; session 0 is services.
func desktopSession() bool {
	var id uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id) != nil {
		return true
	}
	return id != 0
}
