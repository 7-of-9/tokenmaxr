//go:build !windows && !darwin

package autostart

// Default has no mechanism here: registering is unsupported.
func Default() *System { return &System{} }
