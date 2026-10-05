//go:build !windows

package homes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// codexMarker brackets the value the login shell prints, so anything its
// profile scripts write to stdout is ignored.
const codexMarker = "__tokenmaxr_codex_home__"

// DefaultCodexLookups are the places beyond this process's environment that
// can set CODEX_HOME: the user's login shell (an app started by launchd or
// the desktop does not inherit what ~/.zprofile or ~/.bash_profile exports)
// and, on macOS, launchd's own environment (launchctl setenv).
func DefaultCodexLookups() []CodexLookup {
	out := []CodexLookup{{Origin: "login shell", Lookup: loginShellCodexHome}}
	if runtime.GOOS == "darwin" {
		out = append(out, CodexLookup{Origin: "launchd", Lookup: launchdCodexHome})
	}
	return out
}

// loginShell is the user's shell: $SHELL, else the platform's default.
func loginShell() string {
	if s := os.Getenv("SHELL"); strings.HasPrefix(s, "/") {
		return s
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

// loginShellArgs are the arguments that make shell a login shell running
// script. csh and tcsh take -l only as their sole flag, so they run script
// without it, from ~/.cshrc or ~/.tcshrc, where csh users set variables.
func loginShellArgs(shell, script string) []string {
	switch filepath.Base(shell) {
	case "csh", "tcsh":
		return []string{"-c", script}
	}
	return []string{"-l", "-c", script}
}

// codexScript prints CODEX_HOME between markers. printenv, unlike
// "$CODEX_HOME", is not an error in csh when the variable is unset, and works
// the same in every shell.
const codexScript = "echo " + codexMarker + "; printenv CODEX_HOME; echo " + codexMarker

// loginShellCodexHome runs the user's login shell with no terminal and no
// CODEX_HOME of ours, and reads back what the shell's profile sets.
func loginShellCodexHome(ctx context.Context) (string, error) {
	shell := loginShell()
	cmd := exec.CommandContext(ctx, shell, loginShellArgs(shell, codexScript)...)
	cmd.Env = withoutVar(os.Environ(), "CODEX_HOME")
	cmd.Stdin = nil
	// A session of its own: the shell has no controlling terminal (a profile
	// cannot prompt through /dev/tty while doctor runs) and leads a process
	// group, so a timeout kills whatever a hung profile started, not only the
	// shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A profile that starts a background job keeps the pipe open: stop
	// waiting for it shortly after the shell itself exits.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if v, ok := between(out, codexMarker); ok {
		return strings.TrimSpace(v), nil
	}
	if err != nil {
		return "", errors.New("login shell: " + err.Error())
	}
	return "", nil
}

// launchdCodexHome is `launchctl getenv CODEX_HOME` (empty when unset).
func launchdCodexHome(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "getenv", "CODEX_HOME")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil // launchctl exits non-zero for an unset variable on some releases
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// between is the text between the first two markers in out.
func between(out []byte, marker string) (string, bool) {
	m := []byte(marker)
	i := bytes.Index(out, m)
	if i < 0 {
		return "", false
	}
	rest := out[i+len(m):]
	j := bytes.Index(rest, m)
	if j < 0 {
		return "", false
	}
	return string(rest[:j]), true
}

func withoutVar(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}
