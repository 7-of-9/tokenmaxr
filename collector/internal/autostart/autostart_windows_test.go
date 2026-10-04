//go:build windows

package autostart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskXML(t *testing.T) {
	o := Options{Name: "n", Exe: `C:\Users\A B\bin\d0m1-collectorw.exe`, Home: `C:\Users\A B\h&1`}
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.Local)
	x := TaskXML(o, `PC\user`, start)
	for _, want := range []string{
		"<UserId>PC\\user</UserId>",
		"<LogonTrigger>",
		"<Interval>PT1M</Interval>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT5M</ExecutionTimeLimit>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		`<Command>&#34;C:\Users\A B\bin\d0m1-collectorw.exe&#34;</Command>`,
		`<Arguments>--home &#34;C:\Users\A B\h&amp;1&#34; run</Arguments>`,
		"<StartBoundary>2026-09-29T10:00:00</StartBoundary>",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %s", want)
		}
	}
	if strings.Contains(x, "<Duration>") {
		t.Error("repetition must be indefinite (no Duration)")
	}

	// App mode: the watchdog every 5 minutes, and no time limit because the
	// launch it makes may be the app itself.
	o.App = true
	x = TaskXML(o, `PC\user`, start)
	for _, want := range []string{
		"<Interval>PT5M</Interval>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		`<Arguments>--home &#34;C:\Users\A B\h&amp;1&#34; app --watchdog</Arguments>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("watchdog XML lacks %s", want)
		}
	}
	for _, bad := range []string{"<LogonTrigger>", "PT1M", " run<"} {
		if strings.Contains(x, bad) {
			t.Errorf("watchdog XML has %s", bad)
		}
	}
}

// TestRealTask registers real per-user TEST tasks and a TEST Run value,
// runs the headless task once and deletes everything. It touches the
// machine's Task Scheduler and registry, so it only runs when
// D0M1_AUTOSTART_TEST points at a d0m1-collectorw.exe build.
func TestRealTask(t *testing.T) {
	exe := os.Getenv("D0M1_AUTOSTART_TEST")
	if exe == "" {
		t.Skip("set D0M1_AUTOSTART_TEST=<path to d0m1-collectorw.exe> to register real test entries")
	}
	// The root-folder name, and a first-level folder like the SPEC's
	// \d0m1\collector (under a test folder, never the production one).
	t.Run("root", func(t *testing.T) { realTask(t, exe, "d0m1-collector-test", "") })
	t.Run("folder", func(t *testing.T) { realTask(t, exe, `\d0m1-collector-test\collector`, "d0m1-collector-test") })
	t.Run("app", func(t *testing.T) { realApp(t, exe) })
}

func realTask(t *testing.T, exe, name, folder string) {
	var tasks schedTasks
	home := filepath.Join(t.TempDir(), "home")
	t.Cleanup(func() {
		tasks.Uninstall(name)
		if tasks.Present(name) {
			t.Errorf("task %s still present after cleanup", name)
		}
	})
	o := Options{Name: name, Exe: exe, Home: home}
	if got, err := tasks.Install(o); err != nil || got != name {
		t.Fatalf("Install: %q %v", got, err)
	}
	if !tasks.Present(name) {
		t.Fatal("task not found after Install")
	}
	// Re-registering is idempotent.
	if _, err := tasks.Install(o); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	xml := queryXML(t, name)
	for _, want := range []string{"PT1M", "IgnoreNew", "PT5M", "InteractiveToken", "LogonTrigger", "TimeTrigger"} {
		if !strings.Contains(xml, want) {
			t.Errorf("registered task lacks %s", want)
		}
	}
	if err := tasks.Start(name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The unenrolled collector creates its state dir and lock, then exits 0.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(home, "collector.lock")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			d, _ := tasks.Describe(name)
			t.Fatalf("task did not run the collector within 30s:\n%s", d)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Wait for the run to finish and check its exit code.
	result, desc := "", ""
	for time.Now().Before(deadline) && result == "" {
		desc, _ = tasks.Describe(name)
		for _, l := range strings.Split(desc, "\n") {
			v, ok := strings.CutPrefix(strings.TrimSpace(l), "Last Result:")
			// 267009 is SCHED_S_TASK_RUNNING, 267011 SCHED_S_TASK_HAS_NOT_RUN.
			if v = strings.TrimSpace(v); ok && v != "267009" && v != "267011" {
				result = v
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if result != "0" {
		t.Errorf("last result %q, want 0:\n%s", result, desc)
	}
	if err := tasks.Uninstall(name); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if tasks.Present(name) {
		t.Fatal("task still present after Uninstall")
	}
	if folder != "" && FolderExists(folder) {
		t.Fatalf("task folder %s left behind", folder)
	}
}

// realApp registers app mode under TEST names (never started: that would
// put a tray icon on the desktop) and checks what Windows stored.
func realApp(t *testing.T, exe string) {
	s := Default()
	o := Options{Name: "d0m1-collector-app-TEST", RunValue: "d0m1-collector-TEST", Exe: exe, Home: filepath.Join(t.TempDir(), "home"), App: true}
	t.Cleanup(func() {
		s.Unregister(o)
		if r := s.Registered(o); r.LoginItem || r.Task {
			t.Errorf("TEST entries left after cleanup: %+v", r)
		}
	})
	if _, err := s.Register(o); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if m := s.Registered(o).Mode("windows"); m != "app" {
		t.Fatalf("mode %s", m)
	}
	v, ok, err := hkcuRun{}.Get(o.RunValue)
	if err != nil || !ok || v != CommandLine(exe, o.AppArgs()) {
		t.Fatalf("Run value %q %v %v", v, ok, err)
	}
	xml := queryXML(t, o.Name)
	for _, want := range []string{"PT5M", "PT0S", "IgnoreNew", "--watchdog"} {
		if !strings.Contains(xml, want) {
			t.Errorf("watchdog task lacks %s", want)
		}
	}
	if err := s.Unregister(o); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if m := s.Registered(o).Mode("windows"); m != "missing" {
		t.Fatalf("after Unregister: %s", m)
	}
}

func queryXML(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("schtasks.exe", "/Query", "/TN", name, "/XML").Output()
	if err != nil {
		t.Fatalf("query xml: %v", err)
	}
	return decodeUTF16(out)
}

func decodeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		var sb strings.Builder
		for i := 2; i+1 < len(b); i += 2 {
			sb.WriteRune(rune(uint16(b[i]) | uint16(b[i+1])<<8))
		}
		return sb.String()
	}
	return string(b)
}
