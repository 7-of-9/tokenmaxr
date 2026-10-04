//go:build !windows

package tzinfo

import (
	"os"
	"path/filepath"
	"strings"
)

func detectPlatform() (Info, bool) {
	// /etc/localtime is a symlink into a zoneinfo tree on macOS
	// (/var/db/timezone/zoneinfo/...) and most Linux distributions.
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if z := zoneFromPath(target); z != "" {
			return Info{IANA: z, Country: CountryForIANA(z), Source: SourceIANA}, true
		}
	}
	// Debian/Ubuntu also record the name in /etc/timezone.
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		if z := cleanIANA(line); z != "" {
			return Info{IANA: z, Country: CountryForIANA(z), Source: SourceIANA}, true
		}
	}
	// Fall back to fully resolving a multi-hop symlink chain.
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if z := zoneFromPath(target); z != "" {
			return Info{IANA: z, Country: CountryForIANA(z), Source: SourceIANA}, true
		}
	}
	return Info{}, false
}

// zoneFromPath returns the zone name after ".../zoneinfo/" in path, or "".
func zoneFromPath(path string) string {
	path = filepath.ToSlash(path)
	i := strings.LastIndex(path, "zoneinfo/")
	if i < 0 {
		return ""
	}
	return cleanIANA(path[i+len("zoneinfo/"):])
}
