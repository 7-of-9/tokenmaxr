package accountusage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// Explicit opt-in reads aggregates through the installed signed-in client;
// it never writes collector state or uploads. Normal test runs stay offline.
func TestReadCodexInstalledClient(t *testing.T) {
	if os.Getenv("D0M1_TEST_LIVE_ACCOUNT_USAGE") != "1" {
		t.Skip("live account read is opt-in")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	rows, err := ReadCodex(ctx, home, paths.CodexHome(home), []byte("test-only-account-hash"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range rows {
		total += row.TotalTokens
	}
	t.Logf("Read %d UTC daily buckets, %d account tokens; no upload", len(rows), total)
}

// TestCodexReaderProcess is a real subprocess fixture: any extra RPC, model
// request, missing initialization capability or wrong sequence fails the read.
func TestCodexReaderProcess(t *testing.T) {
	mode := os.Getenv("D0M1_TEST_ACCOUNT_READER")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	methods := []string{"initialize", "initialized", "account/usage/read"}
	for i, want := range methods {
		if !scanner.Scan() {
			os.Exit(10)
		}
		var req struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Method != want {
			os.Exit(11)
		}
		if i == 0 {
			caps, _ := req.Params["capabilities"].(map[string]any)
			if caps["experimentalApi"] != true || caps["explicitGatewayOauth"] != true {
				os.Exit(12)
			}
			fmt.Println(`{"id":1,"result":{}}`)
		} else if i == 2 {
			if len(req.Params) != 0 {
				os.Exit(13)
			}
			switch mode {
			case "wait":
				time.Sleep(10 * time.Second)
			case "error":
				fmt.Println(`{"id":2,"error":{"code":-32601,"message":"secret@example.test access-token"}}`)
			default:
				fmt.Println(`{"method":"account/updated","params":{}}`)
				fmt.Println(`{"id":2,"result":{"dailyUsageBuckets":[{"startDate":"2026-09-01","tokens":14000000000}]}}`)
			}
		}
	}
	os.Exit(0)
}

func TestAccountReaderProtocolAndTimeout(t *testing.T) {
	for _, mode := range []string{"ok", "error", "wait"} {
		t.Run(mode, func(t *testing.T) {
			duration := 3 * time.Second
			if mode == "wait" {
				duration = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexReaderProcess$")
			cmd.Env = append(os.Environ(), "D0M1_TEST_ACCOUNT_READER="+mode)
			hideCommand(cmd)
			start := time.Now()
			raw, err := readUsage(ctx, cmd)
			if mode == "ok" {
				if err != nil || !strings.Contains(string(raw), "14000000000") {
					t.Fatalf("read failed: %s %v", raw, err)
				}
			} else {
				if err == nil {
					t.Fatal("expected unavailable/timeout error")
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "access-token") {
					t.Fatal("provider details leaked")
				}
				if time.Since(start) > 4*time.Second {
					t.Fatal("reader exceeded timeout")
				}
			}
		})
	}
}

func TestSnapshotsIdentityValidationAndNoFabricatedSplit(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.FixedZone("UTC+7", 7*3600))
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codex, "auth.json"), []byte(`{"tokens":{"account_id":"shared-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	acct := Account(home, codex, []byte("fleet-key"))
	if acct != model.AccountHash([]byte("fleet-key"), "openai", "shared-account") {
		t.Fatal("different account hash from local events")
	}
	raw := json.RawMessage(`{"dailyUsageBuckets":[{"startDate":"2026-09-02","tokens":14},{"startDate":"2026-09-01","tokens":12},{"startDate":"2026-09-01","tokens":10}]}`)
	rows, err := snapshots(raw, acct, now)
	if err != nil || len(rows) != 2 || rows[0].TotalTokens != 12 || rows[0].Date != "2026-09-01" {
		t.Fatalf("snapshots: %+v %v", rows, err)
	}
	other, _ := snapshots(raw, acct, now.Add(time.Minute))
	if rows[0].ID != other[0].ID {
		t.Fatal("daily snapshot ids changed with observation time")
	}
	wire, _ := json.Marshal(rows[0])
	for _, key := range []string{`"machine"`, `"in"`, `"out"`, `"model"`, `"ws"`, "shared-account"} {
		if strings.Contains(string(wire), key) {
			t.Fatalf("fabricated attribution or raw identity: %s", wire)
		}
	}
	for _, bad := range []string{`{}`, `{"dailyUsageBuckets":[{"startDate":"2026-09-31","tokens":1}]}`, `{"dailyUsageBuckets":[{"startDate":"2026-09-01","tokens":-1}]}`, `{"dailyUsageBuckets":[{"startDate":"2027-01-01","tokens":1}]}`} {
		if _, err := snapshots(json.RawMessage(bad), acct, now); err == nil {
			t.Fatalf("accepted invalid response: %s", bad)
		}
	}
}
