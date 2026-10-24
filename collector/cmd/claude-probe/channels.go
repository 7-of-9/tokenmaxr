package main

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claudeweb"
)

// channelRow is one channel's (or one channel-month's) aggregate.
type channelRow struct {
	In     int64 `json:"in"`
	CacheW int64 `json:"cacheW"`
	CacheR int64 `json:"cacheR"`
	Out    int64 `json:"out"`
	Total  int64 `json:"total"`
	Events int64 `json:"events"`
}

// promptRow counts prompts (activity events).
type promptRow struct {
	Prompts        int64 `json:"prompts"`
	PromptsNoUsage int64 `json:"promptsNoUsage"`
}

func (r *channelRow) add(u model.UsageEvent) {
	r.In += u.In
	r.CacheW += u.CacheW
	r.CacheR += u.CacheR
	r.Out += u.Out
	r.Total += u.In + u.CacheW + u.CacheR + u.Out
	r.Events++
}

type channelReport struct {
	Home  string         `json:"home"`
	Files map[string]int `json:"files"`
	// Channels is source -> quality ("exact"/"estimated") -> totals.
	Channels map[string]map[string]*channelRow `json:"channels"`
	// Months is source -> quality -> local YYYY-MM -> totals.
	Months map[string]map[string]map[string]*channelRow `json:"months"`
	// Prompts is source -> local YYYY-MM -> prompts (activity events).
	Prompts map[string]map[string]*promptRow `json:"prompts"`
	// Range is the first and last local date with usage.
	Range [2]string `json:"range"`
	// UsageIDs is the number of usage events and a digest of their sorted
	// ids, so two runs can show that a re-routing kept every id.
	UsageIDs struct {
		Count  int    `json:"count"`
		Digest string `json:"sha256"`
	} `json:"usageIds"`
}

// channels runs every Claude reader the collector ships over one home,
// read-only, with prompt text off, merges events by id as the server does
// (scan.MergeUsage: a higher pv replaces) and tabulates them by channel,
// quality and local month. Nothing is written or sent.
func channels(home, web string) (*channelReport, error) {
	env := &sources.Env{Home: home, Machine: "probe", Prompts: false, WebClaude: web,
		TZOffsetMin: func(t time.Time) int { _, off := t.In(time.Local).Zone(); return off / 60 },
		HashID:      func(provider, id string) string { return provider + ":" + id },
		Attribute:   func(string, time.Time, string, sources.Hint) (string, string) { return "", model.AcctUnknown },
	}
	srcs := []sources.Source{claude.New(), claude.NewNative()}
	if web != "" {
		srcs = append(srcs, claudeweb.New())
	}
	usage := map[string]model.UsageEvent{}
	activity := map[string]model.ActivityEvent{}
	rep := &channelReport{Home: home, Files: map[string]int{},
		Channels: map[string]map[string]*channelRow{},
		Months:   map[string]map[string]map[string]*channelRow{},
		Prompts:  map[string]map[string]*promptRow{}}
	for _, src := range srcs {
		if err := src.Prepare(env); err != nil {
			return nil, err
		}
		files, err := src.Files(env)
		if err != nil {
			return nil, err
		}
		rep.Files[src.Name()] = len(files)
		for _, f := range files {
			b, _, err := src.Parse(env, f, sources.Cursor{})
			if err != nil {
				return nil, err
			}
			for _, u := range b.Usage {
				if have, ok := usage[u.ID]; ok {
					scan.MergeUsage(&have, u)
					u = have
				}
				usage[u.ID] = u
			}
			for _, a := range b.Activity {
				if have, ok := activity[a.ID]; ok {
					a.HasUsage = a.HasUsage || have.HasUsage
					if have.TS.Before(a.TS) {
						a.TS = have.TS
					}
				}
				activity[a.ID] = a
			}
		}
	}
	month := func(ts time.Time, off int) string {
		return ts.Add(time.Duration(off) * time.Minute).UTC().Format("2006-01")
	}
	first, last := "", ""
	for _, u := range usage {
		q := u.Q
		if q == "" {
			q = model.QualityExact
		}
		if rep.Channels[u.Source] == nil {
			rep.Channels[u.Source] = map[string]*channelRow{}
			rep.Months[u.Source] = map[string]map[string]*channelRow{}
		}
		if rep.Channels[u.Source][q] == nil {
			rep.Channels[u.Source][q] = &channelRow{}
			rep.Months[u.Source][q] = map[string]*channelRow{}
		}
		rep.Channels[u.Source][q].add(u)
		m := month(u.TS, u.TZOffsetMin)
		if rep.Months[u.Source][q][m] == nil {
			rep.Months[u.Source][q][m] = &channelRow{}
		}
		rep.Months[u.Source][q][m].add(u)
		d := u.TS.Add(time.Duration(u.TZOffsetMin) * time.Minute).UTC().Format("2006-01-02")
		if first == "" || d < first {
			first = d
		}
		if d > last {
			last = d
		}
	}
	rep.Range = [2]string{first, last}
	for _, a := range activity {
		if rep.Prompts[a.Source] == nil {
			rep.Prompts[a.Source] = map[string]*promptRow{}
		}
		m := month(a.TS, a.TZOffsetMin)
		r := rep.Prompts[a.Source][m]
		if r == nil {
			r = &promptRow{}
			rep.Prompts[a.Source][m] = r
		}
		r.Prompts++
		if !a.HasUsage {
			r.PromptsNoUsage++
		}
	}
	h := sha256.New()
	for _, id := range slices.Sorted(maps.Keys(usage)) {
		h.Write([]byte(id + "\n"))
	}
	rep.UsageIDs.Count, rep.UsageIDs.Digest = len(usage), hex.EncodeToString(h.Sum(nil))
	// Every channel the dashboards show appears, recorded or not.
	for _, s := range []string{model.SourceClaudeCode, model.SourceClaudeWeb, model.SourceClaudeNative, model.SourceClaudeChat} {
		if rep.Channels[s] == nil {
			rep.Channels[s] = map[string]*channelRow{}
		}
	}
	return rep, nil
}
