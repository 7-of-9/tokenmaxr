package gemini

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// TestRealData parses this machine's real Gemini CLI sessions and logs only
// aggregate numbers (never text, emails or ids), plus whether the SPEC token
// identity holds on every message. Run with D0M1_REALDATA=1.
func TestRealData(t *testing.T) {
	if os.Getenv("D0M1_REALDATA") != "1" {
		t.Skip("set D0M1_REALDATA=1 to parse this machine's real Gemini CLI sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	env := &sources.Env{
		Home:        home,
		Machine:     "realdata",
		Attribute:   func(string, time.Time, string, sources.Hint) (string, string) { return "", model.AcctUnknown },
		Label:       func(string) string { return "" },
		TZOffsetMin: func(ts time.Time) int { _, off := ts.In(time.Local).Zone(); return off / 60 },
		Prompts:     true,
	}
	src := New()
	files, err := src.Files(env)
	if err != nil {
		t.Fatal(err)
	}
	var (
		msgs, withTokens, identityOK, cachedLEinput int
		usage, activity, prompts, withUsage         int
		sum                                         model.Tokens
		models                                      = map[string]int{}
	)
	start := time.Now()
	for _, f := range files {
		data, err := jsonl.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s session
		if err := json.Unmarshal(data, &s); err != nil {
			t.Logf("%d bytes: not a session document", len(data))
			continue
		}
		msgs += len(s.Messages)
		for _, m := range s.Messages {
			if m.Tokens == nil {
				continue
			}
			withTokens++
			tk := *m.Tokens
			if tk.Input+tk.Output+tk.Thoughts+tk.Tool == tk.Total {
				identityOK++
			}
			if tk.Cached <= tk.Input {
				cachedLEinput++
			}
			models[m.Model]++
		}
		b, _, err := src.Parse(env, f, sources.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		usage += len(b.Usage)
		activity += len(b.Activity)
		prompts += len(b.Prompts)
		for _, u := range b.Usage {
			sum.In += u.In
			sum.CacheR += u.CacheR
			sum.Out += u.Out
			sum.Reasoning += u.Reasoning
			sum.Calls += u.Calls
		}
		for _, a := range b.Activity {
			if a.HasUsage {
				withUsage++
			}
		}
	}
	t.Logf("files=%d messages=%d withTokens=%d identity(total==input+output+thoughts+tool)=%d cached<=input=%d parse=%s",
		len(files), msgs, withTokens, identityOK, cachedLEinput, time.Since(start).Round(time.Millisecond))
	t.Logf("usage=%d activity=%d (withUsage %d) prompts=%d models=%d", usage, activity, withUsage, prompts, len(models))
	t.Logf("tokens in=%d cacheR=%d out=%d reasoning=%d calls=%d", sum.In, sum.CacheR, sum.Out, sum.Reasoning, sum.Calls)
	if identityOK != withTokens {
		t.Errorf("SPEC identity fails on %d of %d messages", withTokens-identityOK, withTokens)
	}
}
