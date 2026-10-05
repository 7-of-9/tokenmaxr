//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func init() { defaultStartMenu = shellStartMenu{} }

// shellStartMenu writes shell links with the shell's own WScript.Shell
// (through Windows PowerShell, hidden), so the link is exactly what
// Explorer makes. The values travel in environment variables: no quoting.
type shellStartMenu struct{}

func (shellStartMenu) Programs() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", errors.New("APPDATA is not set")
	}
	return StartMenuPrograms(appData), nil
}

const createLink = `$ErrorActionPreference = 'Stop'
$l = (New-Object -ComObject WScript.Shell).CreateShortcut($env:TOKENMAXR_LNK)
$l.TargetPath = $env:TOKENMAXR_TARGET
$l.Arguments = $env:TOKENMAXR_ARGS
$l.WorkingDirectory = $env:TOKENMAXR_DIR
$l.IconLocation = $env:TOKENMAXR_ICON + ',0'
$l.Description = $env:TOKENMAXR_DESC
$l.Save()`

func (shellStartMenu) Create(s Shortcut) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(powershell(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", createLink)
	cmd.Env = append(os.Environ(),
		"TOKENMAXR_LNK="+s.Path, "TOKENMAXR_TARGET="+s.Target, "TOKENMAXR_ARGS="+s.Args,
		"TOKENMAXR_DIR="+s.Dir, "TOKENMAXR_ICON="+s.Icon, "TOKENMAXR_DESC="+s.Description)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("start menu shortcut: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !fileExists(s.Path) {
		return fmt.Errorf("start menu shortcut: %s was not written", s.Path)
	}
	shellChanged()
	return nil
}

var pSHChangeNotify = windows.NewLazySystemDLL("shell32.dll").NewProc("SHChangeNotify")

// shellChanged tells the shell that icons may have changed
// (SHCNE_ASSOCCHANGED), so Explorer and the Start menu look again rather
// than keep a cached icon. Best effort.
func shellChanged() {
	const shcneAssocChanged, shcnfIDList = 0x08000000, 0
	if pSHChangeNotify.Find() == nil {
		pSHChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
	}
}

func (shellStartMenu) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// powershell is Windows PowerShell from System32 (never one on PATH).
func powershell() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if fileExists(p) {
			return p
		}
	}
	return "powershell.exe"
}
