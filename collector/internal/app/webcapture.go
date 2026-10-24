package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/webcapture"
)

// hostExe is the binary Chrome starts as the native host: the console
// build next to this one on Windows (the windowsgui app has no stdio of
// its own), otherwise this binary.
func hostExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	console := filepath.Join(filepath.Dir(exe), paths.ExeName(false))
	if _, err := os.Stat(console); err == nil {
		return console, nil
	}
	return exe, nil
}

// InstallWebCapture installs or repairs Claude Web capture for this state
// directory.
func (a *App) InstallWebCapture(dev bool) (webcapture.Install, error) {
	exe, err := hostExe()
	if err != nil {
		return webcapture.Install{}, err
	}
	in, err := webcapture.Do(webcapture.Options{Home: a.Home, Exe: exe, Dev: dev})
	if err == nil {
		a.Log.Printf("web capture: installed host %s", in.Host)
	}
	return in, err
}

// promptState describes where Claude chat prompt text goes, for the CLI.
func promptState(s webcapture.Status) string {
	switch {
	case s.SweepPrompts:
		return "on for chats in this browser and for swept history: their prompt text goes to the encrypted prompt archive"
	case s.LivePrompts:
		return "on for chats in this browser: their prompt text goes to the encrypted prompt archive (swept history: off)"
	case s.Prompts != "off" && !s.PromptCapture:
		return "off (set to " + s.Prompts + ", but tokenmaxr's prompt capture is off)"
	}
	return "off (no chat text is kept)"
}

// parseHistoryBefore reads a `web-capture history-before` value: a local
// date, an RFC 3339 time, or off.
func parseHistoryBefore(v string) (time.Time, error) {
	if v == "off" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("web capture: history-before takes a date (YYYY-MM-DD), an RFC 3339 time or off")
}

// WebCaptureCLI is `web-capture install|status|uninstall|sweeper|infer-apps|
// prompts|history-before`.
func (a *App) WebCaptureCLI(sub string, args []string, dev, asJSON bool) error {
	switch sub {
	case "install":
		in, err := a.InstallWebCapture(dev)
		if err != nil {
			return err
		}
		a.printf("Claude Web capture is installed (native host %s).\n\n", in.Host)
		a.printf("Load the extension once in Chrome:\n")
		a.printf("  1. Open chrome://extensions\n")
		a.printf("  2. Turn on Developer mode (top right)\n")
		a.printf("  3. Click \"Load unpacked\" and choose:\n     %s\n\n", in.ExtensionDir)
		a.printf("Then use claude.ai as usual. `%s web-capture status` shows when records arrive.\n", buildinfo.Product)
		a.printf("Token figures are estimates (claude.ai reports no token counts for chat).\n")
		a.printf("Chat prompt text: %s.\n", promptState(webcapture.Read(a.Home, a.Now())))
		a.printf("  `%s web-capture prompts live|all|off` changes it (all includes swept history).\n", buildinfo.Product)
		return nil
	case "uninstall":
		if err := webcapture.Remove(a.Home); err != nil {
			return err
		}
		a.printf("Claude Web capture removed (captured numbers kept, prompt text deleted). Remove the extension from chrome://extensions too.\n")
		return nil
	case "prompts":
		if err := webcapture.SetPrompts(a.Home, args[0]); err != nil {
			return err
		}
		a.printf("Chat prompt text: %s.\nThe extension picks this up within 10 minutes.\n", promptState(webcapture.Read(a.Home, a.Now())))
		return nil
	case "history-before":
		t, err := parseHistoryBefore(args[0])
		if err == nil {
			err = webcapture.SetChatBefore(a.Home, t)
		}
		if err != nil {
			return err
		}
		if t.IsZero() {
			a.printf("Swept turns before this machine's capture start are Claude chat, client not recorded.\n")
		} else {
			a.printf("Swept turns before %s are Claude chat, client not recorded; later ones before this machine's capture start are skipped.\n", t.Local().Format("2006-01-02 15:04"))
		}
		return nil
	case "sweeper", "infer-apps":
		on := len(args) == 1 && args[0] == "on"
		var err error
		if sub == "sweeper" {
			err = webcapture.SetSweeper(a.Home, on)
		} else {
			err = webcapture.SetInferApps(a.Home, on)
		}
		if err != nil {
			return err
		}
		a.printf("%s is %s. The extension picks this up within 10 minutes.\n", sub, map[bool]string{true: "on", false: "off"}[on])
		return nil
	}
	s := webcapture.Read(a.Home, a.Now())
	if asJSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	switch s.State {
	case webcapture.StateNotInstalled:
		a.printf("Claude Web capture: not installed (`%s web-capture install`).\n", buildinfo.Product)
		return nil
	case webcapture.StateWaiting:
		a.printf("Claude Web capture: waiting for the extension. Load it unpacked from\n  %s\n", s.ExtensionDir)
	default:
		a.printf("Claude Web capture: active (extension %s, last seen %s)\n", s.Extension, ago(s.LastSeen, a.Now()))
	}
	if !s.Registered {
		a.printf("  The native host %s is not registered: run install again.\n", s.Host)
	}
	a.printf("  records today: %d, total: %d, last record: %s\n", s.RecordsToday, s.Records, ago(s.LastRecord, a.Now()))
	a.printf("  sweeper: %v, infer apps: %v\n", s.Sweeper, s.InferApps)
	a.printf("  chat prompt text: %s\n", promptState(s))
	if !s.T0.IsZero() {
		a.printf("  capture start (T0): %s\n", s.T0.Local().Format("2006-01-02 15:04"))
	}
	if !s.ChatBefore.IsZero() {
		a.printf("  fleet capture start (history-before): %s\n", s.ChatBefore.Local().Format("2006-01-02 15:04"))
	} else if s.Sweeper {
		a.printf("  history-before: not set. With more than one machine capturing claude.ai and GitHub-only\n")
		a.printf("  publishing, set it to the earliest capture start (T0) in the fleet.\n")
	}
	return nil
}
