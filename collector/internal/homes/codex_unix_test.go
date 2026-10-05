//go:build !windows

package homes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeShell writes an executable stand-in for the user's login shell that
// runs body (sh) and points $SHELL at it.
func fakeShell(t *testing.T, body string) { fakeShellNamed(t, "shell", body) }

func fakeShellNamed(t *testing.T, name, body string) {
	t.Helper()
	sh := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(sh, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
}

// The login shell is asked with -l -c and none of this process's CODEX_HOME;
// what its profile prints around the value is ignored.
func TestLoginShellCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/from/this/process")
	// The stand-in profile exports CODEX_HOME and chatters, then runs the command.
	fakeShell(t, `[ "$1" = "-l" ] && [ "$2" = "-c" ] || exit 9
[ -z "$CODEX_HOME" ] || { echo "inherited $CODEX_HOME"; exit 8; }
echo "Welcome back"
CODEX_HOME=/Volumes/Work/codex; export CODEX_HOME
eval "$3"
echo "bye"`)
	v, err := loginShellCodexHome(context.Background())
	if err != nil || v != "/Volumes/Work/codex" {
		t.Fatalf("%q %v", v, err)
	}
	// Unset in the profile: empty, not an error.
	fakeShell(t, `eval "$3"`)
	if v, err := loginShellCodexHome(context.Background()); v != "" || err != nil {
		t.Fatalf("unset: %q %v", v, err)
	}
	// A shell that cannot run the command: an error for the notes.
	fakeShell(t, `exit 3`)
	if _, err := loginShellCodexHome(context.Background()); err == nil {
		t.Fatal("a failing shell is not reported")
	}
	// A profile that hangs is cut off by the context, with what it started.
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeShell(t, `sleep 30 & echo $! > '`+pidFile+`'
wait`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := loginShellCodexHome(ctx); err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("hung shell: %v after %s", err, time.Since(start))
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the profile's child %d outlived the timeout", pid)
		}
	}
}

// csh and tcsh accept -l only as their sole flag: they run the command
// without it, and an unset CODEX_HOME is empty there too.
func TestLoginShellCodexHomeCsh(t *testing.T) {
	for _, name := range []string{"csh", "tcsh"} {
		fakeShellNamed(t, name, `[ "$1" = "-c" ] && [ $# -eq 2 ] || exit 9
CODEX_HOME=/home/u/codex; export CODEX_HOME
eval "$2"`)
		if v, err := loginShellCodexHome(context.Background()); err != nil || v != "/home/u/codex" {
			t.Fatalf("%s: %q %v", name, v, err)
		}
	}
	if got := loginShellArgs("/usr/bin/zsh", "x"); strings.Join(got, " ") != "-l -c x" {
		t.Fatalf("zsh args %q", got)
	}
}

func TestBetweenAndWithoutVar(t *testing.T) {
	if v, ok := between([]byte("noise\n"+codexMarker+"/x y"+codexMarker+"\nmore"), codexMarker); !ok || v != "/x y" {
		t.Fatalf("%q %v", v, ok)
	}
	if _, ok := between([]byte("only "+codexMarker), codexMarker); ok {
		t.Fatal("one marker is no value")
	}
	env := withoutVar([]string{"A=1", "CODEX_HOME=/x", "CODEX_HOMEX=2"}, "CODEX_HOME")
	if strings.Join(env, " ") != "A=1 CODEX_HOMEX=2" {
		t.Fatalf("%q", env)
	}
	want := 1
	if runtime.GOOS == "darwin" {
		want = 2 // and launchd
	}
	if n := len(DefaultCodexLookups()); n != want {
		t.Fatalf("%d lookups", n)
	}
}
