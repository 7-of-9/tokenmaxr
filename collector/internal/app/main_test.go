package app

import (
	"os"
	"sync"
	"testing"
)

// browserOpens records what the package tried to open in a browser: tests
// never open a real tab (a sign-in test without its own OpenURL used to open
// GitHub's device page on the developer's machine).
var browserOpens struct {
	sync.Mutex
	urls []string
}

// fakeStartMenu is a Start menu in a temp folder.
type fakeStartMenu struct {
	mu       sync.Mutex
	programs string
	made     []Shortcut
	removed  []string
}

func (f *fakeStartMenu) Programs() (string, error) { return f.programs, nil }
func (f *fakeStartMenu) Create(s Shortcut) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made = append(f.made, s)
	os.MkdirAll(f.programs, 0o755)
	return os.WriteFile(s.Path, []byte(s.Target+" "+s.Args), 0o600)
}
func (f *fakeStartMenu) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, path)
	return os.Remove(path)
}

func TestMain(m *testing.M) {
	// Nor the real Start menu.
	dir, err := os.MkdirTemp("", "tokenmaxr-startmenu-test")
	if err != nil {
		panic(err)
	}
	defaultStartMenu = &fakeStartMenu{programs: dir}
	openBrowser = func(target string) error {
		browserOpens.Lock()
		browserOpens.urls = append(browserOpens.urls, target)
		browserOpens.Unlock()
		return nil
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
