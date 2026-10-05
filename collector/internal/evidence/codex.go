package evidence

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const openai = model.ProviderOpenAI

var (
	rolloutName = regexp.MustCompile(`^rollout-.*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)
	keyMeta     = []byte(`"session_meta"`)
	keyPlan     = []byte(`"plan_type"`)
	reloadRe    = regexp.MustCompile(`Reloading auth for account ([A-Za-z0-9_.:-]+)`)
)

// codexLine is the only shape decoded from a rollout line.
type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type             string `json:"type"`
		ID               string `json:"id"`
		CreatorAccountID string `json:"creator_account_id"`
		RateLimits       *struct {
			PlanType string `json:"plan_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

func (h *harvester) codexDir() string {
	if h.o.CodexHome != "" {
		return h.o.CodexHome
	}
	return filepath.Join(h.o.Home, ".codex")
}

func (h *harvester) codex() bool {
	dir := h.codexDir()
	// Linked session folders are followed, each real folder once (fsx.WalkFollow).
	var files []string
	fsx.WalkFollow([]string{filepath.Join(dir, "sessions"), filepath.Join(dir, "archived_sessions")}, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && rolloutName.MatchString(d.Name()) {
			files = append(files, p)
		}
		return nil
	})
	for _, p := range files {
		st, changed := h.changed(p)
		if !changed {
			continue
		}
		if h.late() {
			return false
		}
		m := h.marks[p]
		if st.Size() < m.Off {
			m = Mark{}
		}
		if h.codexRollout(p, &m) == nil {
			m.Size, m.MtimeNs = st.Size(), st.ModTime().UnixNano()
			h.marks[p] = m
			h.res.Files++
		}
	}
	h.codexThreads(filepath.Join(dir, "state_5.sqlite"))
	h.codexAuthLog(filepath.Join(dir, "logs_2.sqlite"))
	return true
}

// codexRollout harvests the first session_meta's creator_account_id (the
// rollout's own identity record) and the plan_type runs of token_count
// lines. Mark.Aux keeps "<meta seen>|<session key>|<last plan>".
func (h *harvester) codexRollout(path string, m *Mark) error {
	r, err := jsonl.Open(path, m.Off)
	if err != nil {
		return err
	}
	defer r.Close()
	metaSeen, stream, lastPlan := false, "", ""
	if parts := strings.SplitN(m.Aux, "|", 3); len(parts) == 3 {
		metaSeen, stream, lastPlan = parts[0] == "1", parts[1], parts[2]
	}
	if stream == "" {
		if mm := rolloutName.FindStringSubmatch(filepath.Base(path)); mm != nil {
			stream = session(openai, mm[1])
		}
	}
	var plan run
	samePlan := func(a, b Record) bool { return a.Plan == b.Plan }
	// A run continued from the previous read starts a new record at its next
	// mark; buildEras treats equal plans of one stream as one.
	for {
		b, ok, err := r.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		isMeta := !metaSeen && bytes.Contains(b, keyMeta)
		if !isMeta && !bytes.Contains(b, keyPlan) {
			continue
		}
		var l codexLine
		if !jsonl.Decode(b, &l) {
			continue
		}
		t, okTS := tsOf(l.Timestamp)
		switch {
		case l.Type == "session_meta" && !metaSeen:
			metaSeen = true
			if l.Payload.ID != "" {
				stream = session(openai, l.Payload.ID)
			}
			if a := h.hash(openai, l.Payload.CreatorAccountID); a != "" && okTS {
				h.out(Record{Provider: openai, Kind: KindSession, Source: SrcCodexCreator, Q: QExact, Acct: a, Stream: stream, TS: t})
			}
		case l.Type == "event_msg" && l.Payload.Type == "token_count" && l.Payload.RateLimits != nil && okTS:
			p := strings.TrimSpace(l.Payload.RateLimits.PlanType)
			if p == "" {
				continue
			}
			lastPlan = p
			plan.add(h, Record{Provider: openai, Kind: KindPlan, Source: SrcCodexPlan, Stream: stream, Plan: p, TS: t}, samePlan)
		}
	}
	plan.flush(h)
	meta := "0"
	if metaSeen {
		meta = "1"
	}
	m.Off = r.Offset()
	m.Aux = meta + "|" + stream + "|" + lastPlan
	return nil
}

// openDB opens a tool's SQLite database read-only and immutable: the
// collector never takes a lock on it.
func openDB(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	u := url.URL{Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?mode=ro&immutable=1&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// codexThreads harvests state_5.threads.creator_account_id per thread.
func (h *harvester) codexThreads(path string) {
	st, changed := h.changed(path)
	if !changed {
		return
	}
	db, err := openDB(path)
	if err != nil {
		return
	}
	defer db.Close()
	ctx := context.Background()
	cols := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info('threads')")
	if err != nil {
		return
	}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			cols[n] = true
		}
	}
	rows.Close()
	if !cols["creator_account_id"] || !cols["id"] {
		h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano()}
		return
	}
	created := "created_at"
	if cols["created_at_ms"] {
		created = "CASE WHEN created_at_ms > 0 THEN created_at_ms ELSE created_at * 1000 END"
	} else if !cols["created_at"] {
		return
	}
	q := "SELECT id, creator_account_id, " + created + " FROM threads WHERE creator_account_id IS NOT NULL AND creator_account_id != ''"
	rows, err = db.QueryContext(ctx, q)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, acct string
		var ms int64
		if rows.Scan(&id, &acct, &ms) != nil || ms <= 0 {
			continue
		}
		if a := h.hash(openai, acct); a != "" {
			h.out(Record{Provider: openai, Kind: KindSession, Source: SrcCodexThreads, Q: QExact, Acct: a,
				Stream: session(openai, id), TS: time.UnixMilli(ms).UTC()})
		}
	}
	h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano()}
	h.res.Files++
}

// codexAuthLog harvests logs_2 "Reloading auth for account" rows newer
// than the stored row id: a sample, and a session record when the row names
// its thread.
func (h *harvester) codexAuthLog(path string) {
	st, changed := h.changed(path)
	if !changed {
		return
	}
	db, err := openDB(path)
	if err != nil {
		return
	}
	defer db.Close()
	m := h.marks[path]
	rows, err := db.QueryContext(context.Background(),
		"SELECT id, ts, ts_nanos, feedback_log_body, COALESCE(thread_id, '') FROM logs WHERE id > ? AND feedback_log_body LIKE '%Reloading auth for account %' ORDER BY id", m.Off)
	if err != nil {
		return
	}
	defer rows.Close()
	last := m.Off
	for rows.Next() {
		var id, sec, nanos int64
		var body, thread string
		if rows.Scan(&id, &sec, &nanos, &body, &thread) != nil {
			continue
		}
		last = max(last, id)
		mm := reloadRe.FindStringSubmatch(body)
		if mm == nil || sec <= 0 {
			continue
		}
		a := h.hash(openai, mm[1])
		t := time.Unix(sec, nanos%1e9).UTC()
		h.out(Record{Provider: openai, Kind: KindSample, Source: SrcCodexAuthLog, Q: QExact, Acct: a, TS: t})
		if thread != "" {
			h.out(Record{Provider: openai, Kind: KindSession, Source: SrcCodexAuthLog, Q: QExact, Acct: a, Stream: session(openai, thread), TS: t})
		}
	}
	h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Off: last, Aux: "id"}
	h.res.Files++
}
