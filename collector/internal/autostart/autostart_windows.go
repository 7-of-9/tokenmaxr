//go:build windows

package autostart

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// Default is the current user's Run key, Task Scheduler and a detached
// process start.
func Default() *System {
	return &System{GOOS: "windows", Run: hkcuRun{}, Tasks: schedTasks{}, Spawn: spawnDetached}
}

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// hkcuRun is HKCU\...\Run.
type hkcuRun struct{}

func (hkcuRun) Get(name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (hkcuRun) Set(name, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

func (hkcuRun) Delete(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// spawnDetached starts exe with no console, outside this console's group,
// so the app outlives the installer's window.
func spawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// schedTasks is Task Scheduler through schtasks.exe. A standard user can
// create the \d0m1 folder on current Windows 11; where policy refuses it,
// Install falls back to FallbackName in the root folder.
type schedTasks struct{}

// candidates lists where a task registered as name may live.
func candidates(name string) []string {
	if name == TaskName {
		return []string{TaskName, FallbackName}
	}
	if name == LegacyTaskName {
		return []string{LegacyTaskName, LegacyFallbackName}
	}
	return []string{name}
}

// resolve returns the registered task for name, or name if none exists.
func resolve(name string) string {
	for _, n := range candidates(name) {
		if exists(n) {
			return n
		}
	}
	return name
}

func exists(name string) bool {
	_, err := schtasks("/Query", "/TN", name)
	return err == nil
}

// hidden runs console children (schtasks.exe) without flashing a window when
// the caller is the windowsgui build.
func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

func schtasks(args ...string) (string, error) {
	out, err := hidden(exec.Command("schtasks.exe", args...)).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("schtasks %s: %v: %s", args[0], err, s)
	}
	return s, nil
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// TaskXML renders the task definition.
//
// Headless: at logon and every minute, one tick of at most 5 minutes.
//
// App mode, the watchdog: every 5 minutes it launches the app. A launch
// while the app runs exits at once on the lock. When the launch is the app
// (it was not running), that copy runs for the session, so the task has no
// time limit and IgnoreNew skips the next launches while it lives.
func TaskXML(o Options, userID string, start time.Time) string {
	var args []string
	for _, a := range o.TaskArgs() {
		args = append(args, winQuote(a))
	}
	desc := buildinfo.Product + ": collects local AI token usage every minute"
	interval, limit := "PT1M", "PT5M"
	logon := `
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(userID) + `</UserId>
    </LogonTrigger>`
	if o.App {
		desc = buildinfo.Product + ": starts the app again if it is not running"
		interval, limit, logon = "PT5M", "PT0S", ""
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>` + esc(desc) + `</Description>
  </RegistrationInfo>
  <Triggers>` + logon + `
    <TimeTrigger>
      <Repetition>
        <Interval>` + interval + `</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
      <StartBoundary>` + start.Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>` + limit + `</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(`"`+o.Exe+`"`) + `</Command>
      <Arguments>` + esc(strings.Join(args, " ")) + `</Arguments>
      <WorkingDirectory>` + esc(filepath.Dir(o.Exe)) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

// utf16File encodes s as UTF-16LE with a BOM, which schtasks /XML expects.
func utf16File(s string) []byte {
	u := utf16.Encode([]rune(strings.ReplaceAll(s, "\n", "\r\n")))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2+2*i:], c)
	}
	return b
}

// Install creates or replaces the task (idempotent) and returns the task
// name actually registered.
func (schedTasks) Install(o Options) (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "d0m1-task-*.xml")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(utf16File(TaskXML(o, u.Username, time.Now().Truncate(time.Minute))))
	f.Close()
	if err != nil {
		return "", err
	}
	names := candidates(o.taskName())
	for i, n := range names {
		if _, err = schtasks("/Create", "/TN", n, "/XML", f.Name(), "/F"); err == nil {
			// Never leave a second copy running from the other location.
			for j, other := range names {
				if j != i {
					deleteTask(other)
				}
			}
			return n, nil
		}
	}
	return "", err
}

func deleteTask(name string) error {
	if !exists(name) {
		return nil
	}
	_, err := schtasks("/Delete", "/TN", name, "/F")
	// A task in a first-level folder (\d0m1\collector) leaves the folder.
	if parts := strings.Split(strings.TrimPrefix(name, `\`), `\`); err == nil && len(parts) == 2 {
		deleteFolder(parts[0])
	}
	return err
}

// deleteFolder removes a first-level task folder if it is empty. schtasks.exe
// cannot delete folders, so this goes through the Schedule.Service COM API.
func deleteFolder(folder string) {
	if folder == "" || strings.ContainsAny(folder, `'\"`) {
		return
	}
	script := `$s=New-Object -ComObject Schedule.Service;$s.Connect();` +
		`$f=$s.GetFolder('\` + folder + `');` +
		`if($f.GetTasks(1).Count -eq 0 -and $f.GetFolders(0).Count -eq 0){$s.GetFolder('\').DeleteFolder('` + folder + `',0)}`
	hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)).Run()
}

// FolderExists reports whether a first-level task folder exists (tests).
func FolderExists(folder string) bool {
	script := `$s=New-Object -ComObject Schedule.Service;$s.Connect();try{$null=$s.GetFolder('\` + folder + `');'yes'}catch{'no'}`
	out, _ := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)).Output()
	return strings.TrimSpace(string(out)) == "yes"
}

// Start runs the task once now (the first headless tick after install).
func (schedTasks) Start(name string) error {
	_, err := schtasks("/Run", "/TN", resolve(name))
	return err
}

// Uninstall deletes the task (wherever Install put it); a missing task is not
// an error.
func (schedTasks) Uninstall(name string) error {
	var first error
	for _, n := range candidates(name) {
		if err := deleteTask(n); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Present reports whether the task exists.
func (schedTasks) Present(name string) bool {
	for _, n := range candidates(name) {
		if exists(n) {
			return true
		}
	}
	return false
}

// Describe returns schtasks' verbose listing for doctor.
func (schedTasks) Describe(name string) (string, error) {
	return schtasks("/Query", "/TN", resolve(name), "/V", "/FO", "LIST")
}
