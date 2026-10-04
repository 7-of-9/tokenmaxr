package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

func envWith(home, q string) *sources.Env {
	return &sources.Env{
		Home:    home,
		Machine: "test-machine",
		Attribute: func(provider string, ts time.Time, sessionID string, _ sources.Hint) (string, string) {
			return "a_" + provider, q
		},
		Label:       func(acct string) string { return "label:" + acct },
		TZOffsetMin: func(time.Time) int { return 0 },
		Prompts:     true,
	}
}

// Only an attribution of rank 3 or more (recorded, session, timeline,
// bounded) labels a prompt; the prompt carries its acctQ, and the account
// hash and the usage/activity attribution are unchanged.
func TestPromptLabelOnlyWhenObserved(t *testing.T) {
	ts := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		q, want string
	}{
		{model.AcctRecorded, "label:a_anthropic"},
		{model.AcctSession, "label:a_anthropic"},
		{model.AcctTimeline, "label:a_anthropic"},
		{model.AcctBounded, "label:a_anthropic"},
		{model.AcctLineage, ""},
		{model.AcctInferred, ""},
		{model.AcctUnknown, ""},
	} {
		e := &Emitter{Env: envWith("", c.q), Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, PV: 1}
		p := e.Prompt("k1", ts, "s1", "", "hello")
		if p.AcctLabel != c.want || p.Acct != "a_anthropic" || p.AcctQ != c.q {
			t.Errorf("acctQ %s: label %q acct %q", c.q, p.AcctLabel, p.Acct)
		}
		e.Activity("k1", ts, "s1", true)
		e.Usage("k1", ts, "s1", "m", model.Tokens{In: 1, Calls: 1})
		if a, u := e.Batch.Activity[0], e.Batch.Usage[0]; a.AcctQ != c.q || u.AcctQ != c.q || a.Acct != "a_anthropic" {
			t.Errorf("acctQ %s: activity %+v usage %+v", c.q, a, u)
		}
		if p.ID != model.EventID(model.KindPrompt, model.ProviderAnthropic, model.SourceClaudeCode, "k1") {
			t.Errorf("prompt id changed")
		}
	}
	if (&Emitter{Provider: "x"}).Prompt("k", ts, "", "", "t").AcctLabel != "" {
		t.Error("no env: no label")
	}
}

// A prompt typed in a sub-folder of a repository carries the repository root.
func TestPromptWorkspaceIsRepoRoot(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "src", "app")
	sub := filepath.Join(repo, "collector")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Emitter{Env: envWith(home, model.AcctTimeline), Provider: model.ProviderOpenAI, Source: model.SourceCodex, PV: 1}
	ts := time.Now()
	if got := e.Prompt("a", ts, "s", sub, "x").Workspace; got != repo {
		t.Errorf("sub-folder: %q, want %q", got, repo)
	}
	loose := filepath.Join(home, "notes")
	if got := e.Prompt("b", ts, "s", loose, "x").Workspace; got != loose {
		t.Errorf("no repo: %q", got)
	}
	if got := e.Prompt("c", ts, "s", "aaaa1111", "x").Workspace; got != "aaaa1111" {
		t.Errorf("hash kept: %q", got)
	}
}

// Usage and activity carry the workspace only as a keyed tag: the same tag
// for the same repo root, none without a key.
func TestWorkspaceTag(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "src", "app")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := envWith(home, model.AcctTimeline)
	env.HashID = func(provider, id string) string { return model.AccountHash([]byte("k"), provider, id) }
	e := &Emitter{Env: env, Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, PV: 1, Dir: repo}
	ts := time.Now()
	e.Usage("u", ts, "s", "m", model.Tokens{Out: 1})
	e.Activity("a", ts, "s", true)
	ws := e.Batch.Usage[0].WS
	if len(ws) != 18 || ws[:2] != "w_" || e.Batch.Activity[0].WS != ws {
		t.Fatalf("tags %q / %q", ws, e.Batch.Activity[0].WS)
	}
	env.HashID = nil
	if e.wsID() != "" {
		t.Fatal("a tag without the fleet key")
	}
}
