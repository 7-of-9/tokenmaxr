// claude-probe exercises the native reader without enrollment, network calls,
// or changes to the running collector. Stdout is aggregate-only. --out writes
// private prompt records (0600) for a loopback ingestion test, never publishing.
// --web reads a Claude Web capture spool instead (estimated, prompt text off).
// --code reads ~/.claude instead (prompt text off). --channels tabulates every
// Claude reader's tokens per channel and month.
package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claudeweb"
)

func main() {
	home, _ := os.UserHomeDir()
	flag.StringVar(&home, "home", home, "OS home to inspect")
	out := flag.String("out", "", "optional private capture file; must not already exist")
	web := flag.String("web", "", "read a Claude Web capture directory (<state>/web-claude) instead of local Cowork transcripts")
	cal := flag.Bool("calibrate", false, "fit the Claude Web estimator's chars-per-token ratio from local Claude Code transcripts (aggregate only)")
	code := flag.Bool("code", false, "read ~/.claude transcripts (Claude Code, and Claude Native for lines the desktop app wrote) instead of local Cowork transcripts; prompt text off")
	ch := flag.Bool("channels", false, "run every Claude reader over --home (and --web if set), read-only with prompt text off, and print tokens per channel, quality and local month (aggregate only)")
	flag.Parse()
	if *ch {
		r, err := channels(home, *web)
		if err == nil {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(r)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *cal {
		c, err := calibrate(home)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(c)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := probe(home, *out, *web, *code); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe(home, path, web string, code bool) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	env := &sources.Env{Home: home, Machine: "Local Claude QA", Prompts: true,
		HashID:    func(provider, id string) string { return model.AccountHash(key, provider, id) },
		WebClaude: web,
		Attribute: func(_ string, _ time.Time, _ string, hint sources.Hint) (string, string) {
			// An org hint (Claude Web) stands in for its account here: the
			// probe has no org-to-account map.
			if hint.ID != "" {
				return hint.ID, model.AcctRecorded
			}
			return "", model.AcctUnknown
		},
	}
	source := claude.NewNative()
	if code {
		source = claude.New()
		env.Prompts = false
		if err := source.Prepare(env); err != nil {
			return err
		}
	}
	if web != "" {
		source = claudeweb.New()
		env.Prompts = false
		if err := source.Prepare(env); err != nil {
			return err
		}
	}
	files, err := source.Files(env)
	if err != nil {
		return err
	}
	usage := map[string]model.UsageEvent{}
	activity := map[string]model.ActivityEvent{}
	prompts := map[string]model.PromptRecord{}
	for _, file := range files {
		b, _, err := source.Parse(env, file, sources.Cursor{})
		if err != nil {
			return err
		}
		for _, u := range b.Usage {
			if old, ok := usage[u.ID]; ok {
				u.Tokens.Max(old.Tokens)
			}
			usage[u.ID] = u
		}
		for _, a := range b.Activity {
			a.HasUsage = a.HasUsage || activity[a.ID].HasUsage
			activity[a.ID] = a
		}
		for _, p := range b.Prompts {
			prompts[p.ID] = p
		}
	}
	var payload struct {
		Usage     []model.UsageEvent         `json:"usage"`
		Activity  []model.ActivityEvent      `json:"activity"`
		Prompts   []model.PromptRecord       `json:"prompts"`
		Source    string                     `json:"source"`
		Published map[string]json.RawMessage `json:"published"`
	}
	payload.Source = source.Name()
	ru := rollup.New()
	var total model.Tokens
	dates, models := map[string]bool{}, map[string]bool{}
	for _, u := range usage {
		ru.AddUsage(u, time.Now())
		payload.Usage = append(payload.Usage, u)
		total.In += u.In
		total.Out += u.Out
		total.CacheW += u.CacheW
		total.CacheR += u.CacheR
		total.CacheW1h += u.CacheW1h
		total.Reasoning += u.Reasoning
		total.Calls += u.Calls
		dates[u.TS.Format("2006-01-02")] = true
		models[u.Model] = true
	}
	for _, a := range activity {
		ru.AddActivity(a)
		payload.Activity = append(payload.Activity, a)
	}
	for _, p := range prompts {
		ru.AddPrompt(p)
		payload.Prompts = append(payload.Prompts, p)
	}
	if path != "" {
		payload.Published = map[string]json.RawMessage{}
		for _, file := range ghpub.Files(ghpub.Machine{ID: "m_000000000001", Label: env.Machine, OS: "darwin", Collector: "local-qa"}, ru.Rows(), ghpub.AccountHistory{}, time.Now()) {
			if !file.Delete {
				payload.Published[file.Path] = json.RawMessage(file.Content)
			}
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		err = json.NewEncoder(f).Encode(payload)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	bySource := map[string]int{}
	for _, u := range usage {
		bySource[u.Source+"/"+u.Q]++
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"source": source.Name(), "files": len(files), "usageBySource": bySource,
		"usageEvents": len(usage), "prompts": len(prompts), "activity": len(activity), "tokens": total,
		"totalTokens": total.In + total.CacheW + total.CacheR + total.Out, "utcDates": dates, "models": models})
}
