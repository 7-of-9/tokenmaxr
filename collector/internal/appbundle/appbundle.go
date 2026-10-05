// Package appbundle keeps tokenmaxr.app in Applications on macOS, so
// Spotlight, Launchpad and `open -a tokenmaxr` find the app by name. The
// bundle holds no copy of the binary: its executable is a script that runs
// the installed binary's `app` command, so a self-update never has to touch
// it. Opened while the app runs, it brings the app's window to the front.
package appbundle

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// Name is the bundle's directory name: what Spotlight matches.
const Name = buildinfo.Product + ".app"

// Identifier marks a bundle as ours: Ensure and Remove touch nothing else.
const Identifier = "com." + buildinfo.Product + ".collector"

const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// Dirs are the places the bundle may live: /Applications when this user can
// write there, else ~/Applications (Spotlight indexes both).
func Dirs() []string {
	var dirs []string
	if writable("/Applications") {
		dirs = append(dirs, "/Applications")
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, "Applications"))
	}
	return dirs
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, "."+buildinfo.Product+"-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Find returns the first of dirs holding a bundle of ours, or "".
func Find(dirs []string) string {
	for _, d := range dirs {
		if p := filepath.Join(d, Name); ours(p) {
			return p
		}
	}
	return ""
}

func ours(bundle string) bool {
	b, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	return err == nil && bytes.Contains(b, []byte("<string>"+Identifier+"</string>"))
}

// Ensure writes or repairs the bundle for the binary exe and returns its
// path; changed reports that a file was written. A bundle of ours is
// updated where it is; a new one goes into the first of dirs.
func Ensure(dirs []string, exe, version string) (path string, changed bool, err error) {
	if len(dirs) == 0 {
		return "", false, errors.New("no Applications folder to write to")
	}
	path = Find(dirs)
	if path == "" {
		path = filepath.Join(dirs[0], Name)
		if _, err := os.Stat(path); err == nil {
			return "", false, fmt.Errorf("%s exists and is not ours", path)
		}
	}
	files := []struct {
		rel  string
		data []byte
		mode os.FileMode
	}{
		{"Contents/Info.plist", []byte(InfoPlist(version)), 0o644},
		{"Contents/MacOS/" + buildinfo.Product, []byte(Launcher(exe)), 0o755},
		{"Contents/Resources/AppIcon.icns", ICNS(), 0o644},
	}
	for _, f := range files {
		p := filepath.Join(path, filepath.FromSlash(f.rel))
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, f.data) {
			if st, err := os.Stat(p); err == nil && st.Mode().Perm() == f.mode {
				continue
			}
		}
		if err := writeFile(p, f.data, f.mode); err != nil {
			return path, changed, err
		}
		changed = true
	}
	if changed {
		// Let LaunchServices (and so Spotlight and `open -a`) see it now.
		register("-f", path)
	}
	return path, changed, nil
}

// register tells LaunchServices about a bundle in an Applications folder
// (a test's temporary folder is never registered).
func register(flag, bundle string) {
	if filepath.Base(filepath.Dir(bundle)) == "Applications" {
		exec.Command(lsregister, flag, bundle).Run()
	}
}

func writeFile(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// Remove deletes every bundle of ours in dirs and returns their paths; a
// bundle with another identifier is left alone.
func Remove(dirs []string) ([]string, error) {
	var removed []string
	var errs []error
	for _, d := range dirs {
		p := filepath.Join(d, Name)
		if !ours(p) {
			continue
		}
		register("-u", p)
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, p)
	}
	return removed, errors.Join(errs...)
}

// InfoPlist is the bundle's Info.plist. LSUIElement keeps the launcher out
// of the Dock: the app it runs decides that (its window mode has a Dock
// icon, tray only has none).
func InfoPlist(version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string>` + Identifier + `</string>
  <key>CFBundleName</key>
  <string>` + buildinfo.Product + `</string>
  <key>CFBundleDisplayName</key>
  <string>` + buildinfo.Product + `</string>
  <key>CFBundleExecutable</key>
  <string>` + buildinfo.Product + `</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>` + xmlText(version) + `</string>
  <key>CFBundleVersion</key>
  <string>` + xmlText(version) + `</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
  <key>LSUIElement</key>
  <true/>
</dict>
</plist>
`
}

// Launcher is the bundle's executable: it runs the installed binary's app,
// which starts the app or, when it already runs, shows its window.
func Launcher(exe string) string {
	return "#!/bin/sh\n# " + buildinfo.Product + ": runs the installed app (a second launch shows it).\nexec " + shQuote(exe) + " app\n"
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func xmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ICNS is the app icon: the Dock tile's green dot at every size Finder
// asks for, as PNG entries.
func ICNS() []byte {
	entries := []struct {
		kind string
		size int
	}{
		{"icp4", 16}, {"icp5", 32}, {"ic11", 32}, {"ic12", 64},
		{"ic07", 128}, {"ic13", 256}, {"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
	}
	var body bytes.Buffer
	for _, e := range entries {
		png := tray.IconPNG(tray.Green, e.size, tray.DockInset)
		body.WriteString(e.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+len(png)))
		body.Write(png)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
