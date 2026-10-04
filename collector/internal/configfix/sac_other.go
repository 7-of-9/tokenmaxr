//go:build !windows

package configfix

// SmartAppControl is Windows-only; other platforms report nothing.
func SmartAppControl() (state, detail string, ok bool) { return "", "", false }
