package autostart

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRun is an in-memory Run key.
type fakeRun map[string]string

func (f fakeRun) Get(name string) (string, bool, error) { v, ok := f[name]; return v, ok, nil }
func (f fakeRun) Set(name, value string) error          { f[name] = value; return nil }
func (f fakeRun) Delete(name string) error              { delete(f, name); return nil }

// fakeTasks is an in-memory Task Scheduler.
type fakeTasks struct {
	tasks   map[string]Options
	started []string
}

func (f *fakeTasks) Install(o Options) (string, error) {
	f.tasks[o.taskName()] = o
	return o.taskName(), nil
}
func (f *fakeTasks) Start(name string) error     { f.started = append(f.started, name); return nil }
func (f *fakeTasks) Uninstall(name string) error { delete(f.tasks, name); return nil }
func (f *fakeTasks) Present(name string) bool    { _, ok := f.tasks[name]; return ok }
func (f *fakeTasks) Describe(name string) (string, error) {
	return "Task To Run: x\nStatus: Ready\nOther: y", nil
}

func TestWindowsModes(t *testing.T) {
	run := fakeRun{"other-app": "x.exe"}
	tasks := &fakeTasks{tasks: map[string]Options{}}
	var spawned [][]string
	s := &System{GOOS: "windows", Run: run, Tasks: tasks, Spawn: func(exe string, args []string) error {
		spawned = append(spawned, append([]string{exe}, args...))
		return nil
	}}
	exe := `C:\Users\A B\AppData\Local\d0m1-collector\bin\d0m1-collectorw.exe`
	o := Options{Name: `\d0m1-TEST\collector`, RunValue: "d0m1-collector-TEST", Exe: exe, Home: `C:\Users\A B\h "q"\`, App: true}

	// App mode: the Run value starts the app, the task is the watchdog.
	if _, err := s.Register(o); err != nil {
		t.Fatal(err)
	}
	want := `"` + exe + `" --home "C:\Users\A B\h \"q\"\\" app`
	if got := run["d0m1-collector-TEST"]; got != want {
		t.Fatalf("Run value\n got %s\nwant %s", got, want)
	}
	if got := tasks.tasks[o.Name].TaskArgs(); !slices.Equal(got, []string{"--home", o.Home, "app", "--watchdog"}) {
		t.Fatalf("watchdog args %q", got)
	}
	if m := s.Registered(o).Mode("windows"); m != "app" {
		t.Fatalf("mode %s", m)
	}
	if err := s.Start(o); err != nil || len(spawned) != 1 || !slices.Equal(spawned[0], []string{exe, "--home", o.Home, "app"}) {
		t.Fatalf("Start: %v %q", err, spawned)
	}
	if d := s.Describe(o); len(d) != 3 || d[0] != "login item: "+want {
		t.Fatalf("Describe %q", d)
	}

	// Headless: the Run value goes, the task runs ticks.
	h := o
	h.App = false
	if _, err := s.Register(h); err != nil {
		t.Fatal(err)
	}
	if _, ok := run["d0m1-collector-TEST"]; ok {
		t.Fatal("Run value kept in headless mode")
	}
	if got := tasks.tasks[o.Name].TaskArgs(); !slices.Equal(got, []string{"--home", o.Home, "run"}) {
		t.Fatalf("headless args %q", got)
	}
	if m := s.Registered(h).Mode("windows"); m != "headless" {
		t.Fatalf("mode %s", m)
	}
	if err := s.Start(h); err != nil || !slices.Equal(tasks.started, []string{o.Name}) {
		t.Fatalf("headless Start: %v %q", err, tasks.started)
	}

	// Unregister removes both modes' entries and nothing else.
	s.Register(o)
	if err := s.Unregister(o); err != nil {
		t.Fatal(err)
	}
	if len(tasks.tasks) != 0 || len(run) != 1 || run["other-app"] != "x.exe" {
		t.Fatalf("after Unregister: tasks %v run %v", tasks.tasks, run)
	}
	if m := s.Registered(o).Mode("windows"); m != "missing" {
		t.Fatalf("mode %s", m)
	}
	// A lone Run value is not app mode: the watchdog is part of it.
	run["d0m1-collector-TEST"] = want
	if m := s.Registered(o).Mode("windows"); m != "missing" {
		t.Fatalf("Run value alone: %s", m)
	}
}

func TestDarwinModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "LaunchAgents")
	var calls []string
	s := &System{GOOS: "darwin", AgentsDir: dir, Domain: "gui/501", Launchctl: func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "state = running\npid = 42\nnoise\n", nil
	}}
	o := Options{Name: "com.d0m1.collector.TEST", Exe: "/Users/a b/Library/Application Support/d0m1-collector/bin/d0m1-collector", App: true}
	if _, err := s.Register(o); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, o.Name+".plist")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(b)
	for _, w := range []string{"<string>" + o.Exe + "</string>\n    <string>app</string>\n  </array>", "<key>RunAtLoad</key>\n  <true/>", "<key>KeepAlive</key>", "<key>SuccessfulExit</key>\n    <false/>", "<string>Aqua</string>"} {
		if !strings.Contains(plist, w) {
			t.Errorf("app plist lacks %q", w)
		}
	}
	for _, bad := range []string{"--watchdog", "StartInterval"} {
		if strings.Contains(plist, bad) {
			t.Errorf("app plist has %q", bad)
		}
	}
	if !slices.Equal(calls, []string{"bootout gui/501/" + o.Name, "bootstrap gui/501 " + p}) {
		t.Fatalf("launchctl %q", calls)
	}
	if m := s.Registered(o).Mode("darwin"); m != "app" {
		t.Fatalf("mode %s", m)
	}
	calls = nil
	if err := s.Start(o); err != nil || !slices.Equal(calls, []string{"kickstart gui/501/" + o.Name}) {
		t.Fatalf("Start: %v %q", err, calls)
	}
	if d := s.Describe(o); len(d) != 3 || d[0] != "LaunchAgent: "+p || d[1] != "state = running" {
		t.Fatalf("Describe %q", d)
	}

	// Headless: the same agent runs a tick every minute at low priority.
	h := o
	h.App = false
	if _, err := s.Register(h); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	for _, w := range []string{"<string>run</string>", "<key>StartInterval</key>\n  <integer>60</integer>", "<key>LowPriorityIO</key>", "<string>Background</string>"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("headless plist lacks %q", w)
		}
	}
	if strings.Contains(string(b), "KeepAlive") {
		t.Error("headless plist keeps the tick alive")
	}
	if m := s.Registered(h).Mode("darwin"); m != "headless" {
		t.Fatalf("mode %s", m)
	}
	calls = nil
	if err := s.Start(h); err != nil || len(calls) != 0 {
		t.Fatalf("headless Start: %v %q", err, calls)
	}

	calls = nil
	if err := s.Unregister(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("plist left: %v", err)
	}
	if !slices.Equal(calls, []string{"bootout gui/501/" + o.Name}) {
		t.Fatalf("launchctl %q", calls)
	}
	if m := s.Registered(o).Mode("darwin"); m != "missing" {
		t.Fatalf("mode %s", m)
	}
}

func TestUnsupported(t *testing.T) {
	s := &System{GOOS: "linux"}
	if _, err := s.Register(Options{App: true}); err == nil {
		t.Fatal("Register on linux")
	}
	if err := s.Unregister(Options{}); err != nil {
		t.Fatal(err)
	}
	if m := s.Registered(Options{}).Mode("linux"); m != "missing" {
		t.Fatal(m)
	}
}

func TestCommandLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`plain`, `plain`},
		{`a b`, `"a b"`},
		{`C:\x y\`, `"C:\x y\\"`},
		{`say "hi"`, `"say \"hi\""`},
		{``, `""`},
	} {
		if got := winQuote(c.in); got != c.want {
			t.Errorf("winQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
