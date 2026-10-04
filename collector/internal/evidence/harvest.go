package evidence

import (
	"os"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// Options says where one home's tools keep their files and how to hash.
type Options struct {
	// Home is the home directory; HomeKey its key in state ("" for the OS
	// home), written into every record.
	Home    string
	HomeKey string
	// CodexHome is the Codex directory ("" means <Home>/.codex).
	CodexHome string
	// Hash is model.AccountHash with the fleet key.
	Hash func(provider, nativeID string) string
	// Deadline stops the harvest between files; zero means none.
	Deadline time.Time
	// ClaudeDesktop overrides the Claude desktop app's
	// claude-code-sessions directory (tests); "" uses the platform's.
	ClaudeDesktop string
	// CursorLogs overrides Cursor's logs directory (tests).
	CursorLogs string
}

// Result is one harvest pass over one home.
type Result struct {
	Records []Record
	// Labels are default private labels for accounts seen in snapshots
	// ("email · org"); they stay in config.json like the probe's.
	Labels map[string]string
	// Complete is false when the deadline stopped the pass early.
	Complete bool
	// Files is how many files were read (not skipped as unchanged).
	Files int
}

type harvester struct {
	o     Options
	marks map[string]Mark
	res   *Result
}

func (h *harvester) out(r Record) {
	r.Home = h.o.HomeKey
	h.res.Records = append(h.res.Records, r)
}

func (h *harvester) label(acct string, parts ...string) {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	if acct == "" || len(keep) == 0 {
		return
	}
	if _, ok := h.res.Labels[acct]; !ok {
		h.res.Labels[acct] = strings.Join(keep, " · ")
	}
}

func (h *harvester) hash(provider, native string) string {
	native = strings.TrimSpace(native)
	if native == "" || h.o.Hash == nil {
		return ""
	}
	return h.o.Hash(provider, native)
}

func (h *harvester) hashOrg(provider, uuid string) string {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return ""
	}
	return h.hash(provider, "org:"+uuid)
}

func (h *harvester) late() bool {
	return !h.o.Deadline.IsZero() && time.Now().After(h.o.Deadline)
}

// changed reports whether path differs from its mark (and returns its stat).
func (h *harvester) changed(path string) (os.FileInfo, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, false
	}
	m, ok := h.marks[path]
	return st, !ok || m.Size != st.Size() || m.MtimeNs != st.ModTime().UnixNano()
}

// Harvest reads one home's identity evidence, resuming from marks (which it
// updates in place). Records are new evidence only; Merge them into the
// stored list.
func Harvest(o Options, marks map[string]Mark) Result {
	res := Result{Labels: map[string]string{}, Complete: true}
	h := &harvester{o: o, marks: marks, res: &res}
	for _, f := range []func() bool{h.claude, h.codex, h.grok, h.gemini, h.cursor} {
		if !f() {
			res.Complete = false
			break
		}
	}
	return res
}

// session hashes a raw session id the way events carry it.
func session(provider, id string) string { return model.SessionKey(provider, id) }

// tsOf reads an RFC 3339 timestamp.
func tsOf(s string) (time.Time, bool) { return jsonl.ParseTime(s) }

// run collects consecutive equal identities in one stream into ranges.
type run struct {
	rec    Record
	active bool
}

func (r *run) add(h *harvester, rec Record, same func(a, b Record) bool) {
	if r.active && same(r.rec, rec) {
		if rec.TS.After(r.rec.To) {
			r.rec.To = rec.TS
		}
		return
	}
	r.flush(h)
	rec.To = rec.TS
	r.rec, r.active = rec, true
}

func (r *run) flush(h *harvester) {
	if r.active {
		if !r.rec.To.After(r.rec.TS) {
			r.rec.To = time.Time{}
		}
		h.out(r.rec)
	}
	r.active = false
}
