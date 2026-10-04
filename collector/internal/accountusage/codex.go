// Package accountusage reads recorded account history through the installed
// provider client. Credentials remain with that client; only hashed account
// identifiers and daily aggregate counts reach the collector's outbox.
package accountusage

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
	"sort"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

var (
	ErrNoAccount = errors.New("Codex account history needs an existing ChatGPT sign-in")
	ErrNoClient  = errors.New("Codex account history needs an installed Codex client")
)

// Account uses exactly the identity resolver and fleet hash used for local
// Codex events. Never substitute an email or a device identity for this id.
func Account(userHome, codexHome string, key []byte) string {
	if o, ok := accounts.CodexAccount(codexHome); ok {
		return accounts.Hash(key, o)
	}
	return ""
}

type usageResponse struct {
	DailyUsageBuckets []struct {
		StartDate string `json:"startDate"`
		Tokens    int64  `json:"tokens"`
	} `json:"dailyUsageBuckets"`
}

// ReadCodex performs only initialize and account/usage/read. It neither starts
// a model/thread nor signs a user in. A missing/old client is a soft failure;
// local session-log collection must continue normally.
func ReadCodex(ctx context.Context, userHome, codexHome string, key []byte, now time.Time) ([]model.AccountUsageSnapshot, error) {
	acct := Account(userHome, codexHome, key)
	if acct == "" {
		return nil, ErrNoAccount
	}
	exe := findCodex(userHome)
	if exe == "" {
		return nil, ErrNoClient
	}
	cmd := exec.CommandContext(ctx, exe, "app-server", "--stdio", "-c", "analytics.enabled=false")
	cmd.Env = withEnv(os.Environ(), "CODEX_HOME", codexHome)
	cmd.Dir = codexHome
	hideCommand(cmd)
	raw, err := readUsage(ctx, cmd)
	if err != nil {
		return nil, err
	}
	// Login can change while the subprocess refreshes/reads credentials. Do
	// not label a response using an identity sampled before that boundary.
	if Account(userHome, codexHome, key) != acct {
		return nil, errors.New("Codex account changed while reading history; retrying later")
	}
	return snapshots(raw, acct, now)
}

func snapshots(raw json.RawMessage, acct string, now time.Time) ([]model.AccountUsageSnapshot, error) {
	var data usageResponse
	if err := json.Unmarshal(raw, &data); err != nil || data.DailyUsageBuckets == nil {
		return nil, errors.New("Codex returned no daily account usage")
	}
	// Duplicate dates are not additional usage; keep their largest observed
	// daily total. Refuse malformed buckets instead of inventing dates/counts.
	days := map[string]int64{}
	for _, day := range data.DailyUsageBuckets {
		d, err := time.Parse("2006-01-02", day.StartDate)
		if err != nil || d.Format("2006-01-02") != day.StartDate || d.After(now.UTC()) || day.Tokens < 0 || day.Tokens > 1e13 {
			return nil, errors.New("Codex returned an invalid daily account usage bucket")
		}
		days[day.StartDate] = max(days[day.StartDate], day.Tokens)
	}
	var dates []string
	for date := range days {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	out := make([]model.AccountUsageSnapshot, 0, len(dates))
	for _, date := range dates {
		out = append(out, model.AccountUsageSnapshot{
			ID:       model.EventID("account-usage", model.ProviderOpenAI, model.SourceCodex, acct+"|UTC|"+date),
			Provider: model.ProviderOpenAI, Source: model.SourceCodex,
			Acct: acct, AcctQ: model.AcctRecorded, Date: date, Timezone: "UTC",
			TotalTokens: days[date], ObservedAt: now.UTC(),
		})
	}
	return out, nil
}

// readUsage runs one short-lived stdio client. Provider stderr and RPC error
// messages are intentionally discarded: they can contain account details.
func readUsage(ctx context.Context, cmd *exec.Cmd) (json.RawMessage, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("could not open Codex account reader")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, errors.New("could not open Codex account reader")
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, errors.New("could not start Codex account reader")
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	type response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	lines := make(chan response)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var r response
			if json.Unmarshal(scanner.Bytes(), &r) != nil || r.ID == 0 {
				continue
			}
			select {
			case lines <- r:
			case <-readCtx.Done():
				return
			}
		}
	}()
	enc := json.NewEncoder(stdin)
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		if err := enc.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, errors.New("Codex account reader stopped")
		}
		for {
			select {
			case <-ctx.Done():
				return nil, errors.New("Codex account history read timed out")
			case r, ok := <-lines:
				if !ok {
					return nil, errors.New("Codex account reader closed without daily usage")
				}
				if r.ID != id {
					continue
				}
				if r.Error != nil {
					return nil, fmt.Errorf("Codex account history unavailable (RPC %d)", r.Error.Code)
				}
				return r.Result, nil
			}
		}
	}
	_, err = rpc(1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "d0m1_collector", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": true, "explicitGatewayOauth": true},
	})
	if err != nil {
		return nil, err
	}
	if err := enc.Encode(map[string]string{"method": "initialized"}); err != nil {
		return nil, errors.New("Codex account reader stopped")
	}
	return rpc(2, "account/usage/read", map[string]any{})
}

func withEnv(env []string, key, value string) []string {
	var out []string
	for _, s := range env {
		name, _, _ := strings.Cut(s, "=")
		if !strings.EqualFold(name, key) {
			out = append(out, s)
		}
	}
	return append(out, key+"="+value)
}

// Prefer native executables over npm .cmd shims so timeout cancellation and
// hidden startup are reliable. GUI launch environments often omit npm/Homebrew
// from PATH, hence the standard install-location fallbacks.
func findCodex(home string) string {
	var candidates []string
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		roots := []string{filepath.Join(home, "AppData", "Roaming", "npm"), filepath.Join(home, ".local", "bin")}
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			roots = append(roots, dir)
		}
		for _, root := range roots {
			candidates = append(candidates, filepath.Join(root, "codex.exe"))
			for _, pattern := range []string{
				filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-*", "vendor", "*", "bin", "codex.exe"),
				filepath.Join(root, "node_modules", "@openai", "codex", "vendor", "*", "bin", "codex.exe"),
			} {
				matches, _ := filepath.Glob(pattern)
				candidates = append(candidates, matches...)
			}
		}
	} else {
		candidates = append(candidates, "/Applications/Codex.app/Contents/Resources/codex", filepath.Join(home, ".local", "bin", "codex"), "/opt/homebrew/bin/codex", "/usr/local/bin/codex")
		// npm's codex executable is a JS launcher. Resolve it to the installed
		// native package so a login agent need not have node on its PATH and
		// cancellation terminates the app server itself, not only its parent.
		for _, candidate := range candidates {
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil && strings.HasSuffix(resolved, ".js") {
				candidates = append(nativeNpmCandidates(filepath.Dir(filepath.Dir(resolved)), "codex"), candidates...)
			}
		}
		for _, packageRoot := range []string{
			"/opt/homebrew/lib/node_modules/@openai/codex", "/usr/local/lib/node_modules/@openai/codex",
			filepath.Join(home, ".npm-global", "lib", "node_modules", "@openai", "codex"),
		} {
			candidates = append(candidates, nativeNpmCandidates(packageRoot, "codex")...)
		}
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func nativeNpmCandidates(packageRoot, executable string) []string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	var out []string
	for _, pattern := range []string{
		filepath.Join(packageRoot, "node_modules", "@openai", "codex-"+runtime.GOOS+"-"+arch, "vendor", "*", "bin", executable),
		filepath.Join(packageRoot, "vendor", "*", "bin", executable),
	} {
		matches, _ := filepath.Glob(pattern)
		out = append(out, matches...)
	}
	return out
}
