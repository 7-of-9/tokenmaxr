//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The shell link the real Start menu gets, written into a temp folder (never
// the user's Start menu) and read back through the shell.
func TestShellStartMenuLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Start Menu", "Programs")
	s := Shortcut{
		Path:        filepath.Join(dir, "tokenmaxr.lnk"),
		Target:      `C:\Users\A B\AppData\Local\tokenmaxr\bin\tokenmaxrw.exe`,
		Args:        `--home "C:\x y" app`,
		Dir:         `C:\Users\A B\AppData\Local\tokenmaxr\bin`,
		Icon:        `C:\Users\A B\AppData\Local\tokenmaxr\bin\tokenmaxr.ico`,
		Description: "tokenmaxr: AI token usage collector",
	}
	sm := shellStartMenu{}
	if err := sm.Create(s); err != nil {
		t.Fatal(err)
	}
	read := exec.Command(powershell(), "-NoProfile", "-NonInteractive", "-Command",
		`$l = (New-Object -ComObject WScript.Shell).CreateShortcut($env:TOKENMAXR_LNK); "$($l.TargetPath)|$($l.Arguments)|$($l.WorkingDirectory)|$($l.IconLocation)"`)
	read.Env = append(os.Environ(), "TOKENMAXR_LNK="+s.Path)
	out, err := read.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	if want := s.Target + "|" + s.Args + "|" + s.Dir + "|" + s.Icon + ",0"; got != want {
		t.Fatalf("link reads\n %s\nwant\n %s", got, want)
	}
	if err := sm.Remove(s.Path); err != nil || fileExists(s.Path) {
		t.Fatalf("remove: %v", err)
	}
	if err := sm.Remove(s.Path); err != nil {
		t.Fatalf("removing a missing link: %v", err)
	}
	if p, err := sm.Programs(); err != nil || !strings.HasSuffix(p, filepath.Join("Microsoft", "Windows", "Start Menu", "Programs")) {
		t.Fatalf("programs %q %v", p, err)
	}
}
