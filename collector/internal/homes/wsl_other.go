//go:build !windows

package homes

// DefaultWSL is nil off Windows: there is no WSL to discover.
func DefaultWSL() *WSL { return nil }
