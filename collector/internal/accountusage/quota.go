package accountusage

// Fresh quota readings through the installed, already signed-in clients.
// Each client fetches from its provider with its own credentials: the
// collector never reads a token or calls a provider API itself, and sends no
// prompt (no model request is made).
//
//   - Claude Code: `claude -p` in stream-json mode, control requests
//     `initialize` then `get_usage` only. Claude Code refreshes its own
//     ~/.claude.json cachedUsageUtilization, which internal/limits reads.
//   - Grok: `grok agent stdio` (ACP), `initialize` then `_x.ai/billing`. Grok
//     records the fresh billing config in ~/.grok/logs/unified.jsonl, which
//     internal/limits reads.
//   - Codex: `codex app-server`, `initialize` then `account/rateLimits/read`.
//     The app server only returns the meter, so it is written in Codex's own
//     rollout format to a collector-owned file that internal/limits also reads.
//
// Provider stderr and RPC error bodies are discarded: they can contain
// account details.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

var (
	ErrNoClaude = errors.New("Claude Code is not installed")
	ErrNoGrok   = errors.New("Grok is not installed")
)

type lineSession struct {
	enc   *json.Encoder
	lines chan []byte
	stop  func()
}

func startLines(ctx context.Context, cmd *exec.Cmd, what string) (*lineSession, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("could not open %s", what)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("could not open %s", what)
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	hideCommand(cmd)
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("could not start %s", what)
	}
	readCtx, cancel := context.WithCancel(ctx)
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-readCtx.Done():
				// Keep draining so a client finishing up never blocks on stdout.
			}
		}
	}()
	return &lineSession{
		enc:   json.NewEncoder(stdin),
		lines: lines,
		// End of input lets the client exit on its own and finish writing
		// the refreshed meter (Claude Code saves ~/.claude.json after it
		// answers); it is killed only if it has not exited within exitGrace.
		stop: func() {
			cancel()
			stdin.Close()
			exited := make(chan struct{})
			go func() { cmd.Wait(); close(exited) }()
			select {
			case <-exited:
			case <-time.After(exitGrace):
				cmd.Process.Kill()
				<-exited
			}
		},
	}, nil
}

// exitGrace is how long a client may take to exit after end of input.
const exitGrace = 3 * time.Second

// await returns the first output line that match accepts.
func (s *lineSession) await(ctx context.Context, what string, match func([]byte) bool) ([]byte, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s timed out", what)
		case line, ok := <-s.lines:
			if !ok {
				return nil, fmt.Errorf("%s closed early", what)
			}
			if match(line) {
				return line, nil
			}
		}
	}
}

// rpc sends one JSON-RPC request and waits for its response. jsonrpc2 adds
// the "jsonrpc":"2.0" member that ACP (Grok) requires; Codex's app server
// uses the bare form.
func (s *lineSession) rpc(ctx context.Context, what string, jsonrpc2 bool, id int, method string, params any) (json.RawMessage, error) {
	req := map[string]any{"id": id, "method": method, "params": params}
	if jsonrpc2 {
		req["jsonrpc"] = "2.0"
	}
	if err := s.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("%s stopped", what)
	}
	line, err := s.await(ctx, what, func(l []byte) bool {
		var r struct {
			ID *int `json:"id"`
		}
		return json.Unmarshal(l, &r) == nil && r.ID != nil && *r.ID == id
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &r) != nil {
		return nil, fmt.Errorf("%s returned an unreadable response", what)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("%s unavailable (RPC %d)", what, r.Error.Code)
	}
	return r.Result, nil
}

// RefreshClaude asks Claude Code to refresh its usage meter. It runs with no
// user settings (so no hooks), no MCP servers and no saved session, in a
// neutral working directory, and sends control requests only.
func RefreshClaude(ctx context.Context, userHome, workDir string) error {
	exe := findClaude(userHome)
	if exe == "" {
		return ErrNoClaude
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return errors.New("could not prepare the Claude quota reader")
	}
	cmd := exec.CommandContext(ctx, exe, "-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence", "--setting-sources", "local", "--strict-mcp-config")
	cmd.Dir = workDir
	const what = "Claude quota read"
	s, err := startLines(ctx, cmd, what)
	if err != nil {
		return err
	}
	defer s.stop()
	control := func(id string, request map[string]any) error {
		if err := s.enc.Encode(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
			return fmt.Errorf("%s stopped", what)
		}
		line, err := s.await(ctx, what, func(l []byte) bool {
			var m struct {
				Type     string `json:"type"`
				Response struct {
					RequestID string `json:"request_id"`
				} `json:"response"`
			}
			return json.Unmarshal(l, &m) == nil && m.Type == "control_response" && m.Response.RequestID == id
		})
		if err != nil {
			return err
		}
		var m struct {
			Response struct {
				Subtype string `json:"subtype"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &m) != nil || m.Response.Subtype != "success" {
			return fmt.Errorf("%s was refused", what)
		}
		return nil
	}
	if err := control("d0m1-init", map[string]any{"subtype": "initialize"}); err != nil {
		return err
	}
	return control("d0m1-usage", map[string]any{"subtype": "get_usage", "skip_behaviors": true})
}

// RefreshGrok asks Grok's agent for its billing config, which Grok records
// in its own log. No session is created and no prompt is sent.
func RefreshGrok(ctx context.Context, userHome, workDir string) error {
	exe := findGrok(userHome)
	if exe == "" {
		return ErrNoGrok
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return errors.New("could not prepare the Grok quota reader")
	}
	cmd := exec.CommandContext(ctx, exe, "agent", "stdio")
	cmd.Dir = workDir
	const what = "Grok quota read"
	s, err := startLines(ctx, cmd, what)
	if err != nil {
		return err
	}
	defer s.stop()
	if _, err := s.rpc(ctx, what, true, 1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
	}); err != nil {
		return err
	}
	result, err := s.rpc(ctx, what, true, 2, "_x.ai/billing", map[string]any{})
	if err != nil {
		return err
	}
	var r struct {
		Config json.RawMessage `json:"config"`
	}
	if json.Unmarshal(result, &r) != nil || len(r.Config) == 0 || string(r.Config) == "null" {
		return fmt.Errorf("%s returned no billing config", what)
	}
	return nil
}

// RefreshCodex reads Codex's current rate limits through its app server and
// writes them to outPath as a two-line Codex rollout (session_meta, then a
// token_count with rate_limits), stamped now.
func RefreshCodex(ctx context.Context, userHome, codexHome, outPath string, now time.Time) error {
	exe := findCodex(userHome)
	if exe == "" {
		return ErrNoClient
	}
	cmd := exec.CommandContext(ctx, exe, "app-server", "--stdio", "-c", "analytics.enabled=false")
	cmd.Env = withEnv(os.Environ(), "CODEX_HOME", codexHome)
	cmd.Dir = codexHome
	const what = "Codex quota read"
	s, err := startLines(ctx, cmd, what)
	if err != nil {
		return err
	}
	defer s.stop()
	if _, err := s.rpc(ctx, what, false, 1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "d0m1_collector", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true, "explicitGatewayOauth": true},
	}); err != nil {
		return err
	}
	if err := s.enc.Encode(map[string]string{"method": "initialized"}); err != nil {
		return fmt.Errorf("%s stopped", what)
	}
	raw, err := s.rpc(ctx, what, false, 2, "account/rateLimits/read", map[string]any{"excludeResetCreditDetails": true})
	if err != nil {
		return err
	}
	body, err := CodexRollout(raw, now)
	if err != nil {
		return err
	}
	return writeAtomic(outPath, body)
}

type codexAppWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int    `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type rolloutWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at,omitempty"`
}

func toRolloutWindow(w *codexAppWindow) *rolloutWindow {
	if w == nil || w.WindowDurationMins == nil || *w.WindowDurationMins <= 0 {
		return nil
	}
	out := &rolloutWindow{UsedPercent: w.UsedPercent, WindowMinutes: *w.WindowDurationMins}
	if w.ResetsAt != nil {
		out.ResetsAt = *w.ResetsAt
	}
	return out
}

// CodexRollout converts an account/rateLimits/read result into the rollout
// lines Codex itself writes, so internal/limits attributes and names the
// meter exactly as it does for Codex's own session files.
func CodexRollout(raw json.RawMessage, now time.Time) ([]byte, error) {
	var r struct {
		AccountID  *string `json:"accountId"`
		RateLimits *struct {
			LimitID   *string         `json:"limitId"`
			LimitName *string         `json:"limitName"`
			PlanType  *string         `json:"planType"`
			Primary   *codexAppWindow `json:"primary"`
			Secondary *codexAppWindow `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(raw, &r) != nil || r.RateLimits == nil {
		return nil, errors.New("Codex quota read returned no rate limits")
	}
	primary, secondary := toRolloutWindow(r.RateLimits.Primary), toRolloutWindow(r.RateLimits.Secondary)
	if primary == nil && secondary == nil {
		return nil, errors.New("Codex quota read returned no rate-limit window")
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	meta := map[string]any{"timestamp": ts, "type": "session_meta", "payload": map[string]any{
		"id": "collector-quota", "creator_account_id": r.AccountID,
	}}
	rates := map[string]any{
		"limit_id": r.RateLimits.LimitID, "limit_name": r.RateLimits.LimitName, "plan_type": r.RateLimits.PlanType,
		"primary": primary, "secondary": secondary,
	}
	count := map[string]any{"timestamp": ts, "type": "event_msg", "payload": map[string]any{
		"type": "token_count", "rate_limits": rates,
	}}
	var out []byte
	for _, line := range []any{meta, count} {
		b, err := json.Marshal(line)
		if err != nil {
			return nil, errors.New("Codex quota reading could not be encoded")
		}
		out = append(append(out, b...), '\n')
	}
	return out, nil
}

func writeAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("could not write the Codex quota reading")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return errors.New("could not write the Codex quota reading")
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return errors.New("could not write the Codex quota reading")
	}
	return nil
}

// findClaude prefers the native executable over npm's .cmd shim, for reliable
// hidden startup and timeout cancellation (see findCodex).
func findClaude(home string) string {
	var candidates []string
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("claude.exe"); err == nil {
			candidates = append(candidates, p)
		}
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "claude.exe"),
			filepath.Join(home, "AppData", "Roaming", "npm", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"),
		)
	} else {
		if p, err := exec.LookPath("claude"); err == nil {
			candidates = append(candidates, p)
		}
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "claude"),
			filepath.Join(home, ".claude", "local", "claude"),
			"/opt/homebrew/bin/claude", "/usr/local/bin/claude",
		)
	}
	return firstFile(candidates)
}

func findGrok(home string) string {
	name := "grok"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidates := []string{filepath.Join(home, ".grok", "bin", name)}
	if p, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, p)
	}
	return firstFile(candidates)
}

func firstFile(candidates []string) string {
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
