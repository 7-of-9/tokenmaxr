// Package webcleanup removes what collector 0.4.16's Claude Web capture may
// have left on a machine. 0.4.17 removed that capture (docs/agents/SPEC.md
// "Retired Claude channels"). `tokenmaxr web-capture install` had written an
// unpacked Chrome extension to <state>/chrome-extension, a launcher, host
// manifest, settings and record spool to <state>/web-claude, and registered
// the launcher as the Chrome native-messaging host com.d0m1.tokenmaxr
// (com.d0m1.tokenmaxr.dev for development builds): under HKCU on Windows,
// as a copy of the host manifest in Chrome's NativeMessagingHosts folders on
// macOS and Linux. Clean takes all of it away, best effort; the collector
// runs it once per process and again on uninstall.
package webcleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// The host names 0.4.16 registered.
const (
	hostName    = "com.d0m1.tokenmaxr"
	devHostName = hostName + ".dev"
)

// The OS registration (register_*.go); tests replace them. registrations
// lists the host manifests Chrome would find for a host name (Windows: the
// one HKCU names; macOS, Linux: each copy in a NativeMessagingHosts folder),
// and unregister removes one of them.
var (
	registrations = osRegistrations
	unregister    = osUnregister
)

// Clean unregisters every native-messaging host that starts this state
// directory's launcher, then deletes the extension folder and the capture
// folder (spool, launcher, host manifests, settings). A host registered by
// another state directory is left alone. It returns what it removed, for the
// log, with the errors of what it could not; a machine that never installed
// the capture gets nil and no error.
func Clean(home string) ([]string, error) {
	var removed []string
	var errs error
	dir := paths.WebClaude(home)
	for _, name := range []string{hostName, devHostName} {
		for _, reg := range registrations(name) {
			if !ours(reg, dir) {
				continue
			}
			if err := unregister(name, reg); err != nil {
				errs = errors.Join(errs, fmt.Errorf("unregistering the Chrome host %s: %w", name, err))
				continue
			}
			removed = append(removed, "Chrome native-messaging host "+name+" ("+reg+")")
		}
	}
	for _, d := range []string{paths.ChromeExtension(home), dir} {
		if _, err := os.Lstat(d); err != nil {
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		removed = append(removed, d)
	}
	return removed, errs
}

// ours reports whether the host manifest at reg belongs to the capture
// folder dir: it lives there (what HKCU names on Windows), or, as Chrome's
// own copy (macOS, Linux), it starts the launcher there. The manifest may be
// gone already; its location still says whose it was.
func ours(reg, dir string) bool {
	if reg == "" {
		return false
	}
	if sameDir(filepath.Dir(reg), dir) {
		return true
	}
	var m struct {
		Path string `json:"path"`
	}
	b, err := os.ReadFile(reg)
	return err == nil && json.Unmarshal(b, &m) == nil && m.Path != "" && sameDir(filepath.Dir(m.Path), dir)
}

func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
