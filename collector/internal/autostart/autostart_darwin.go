//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Default is ~/Library/LaunchAgents and launchctl in the GUI domain.
func Default() *System {
	s := &System{GOOS: "darwin", Launchctl: launchctl, Domain: "gui/" + strconv.Itoa(os.Getuid())}
	if h, err := os.UserHomeDir(); err == nil {
		s.AgentsDir = filepath.Join(h, "Library", "LaunchAgents")
	}
	return s
}

// launchctl runs launchctl; the output is capped for doctor.
func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	s := string(out)
	if len(s) > 4000 {
		s = s[:4000]
	}
	if err != nil {
		return s, fmt.Errorf("launchctl %s: %v: %s", args[0], err, strings.TrimSpace(s))
	}
	return s, nil
}
