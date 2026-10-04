// Package grok reads Grok CLI sessions (~/.grok/sessions/<cwd>/<sessionUuid>/):
// updates.jsonl turn_completed usage and user prompts, with usage.json as the
// fallback for sessions that have no turn_completed lines. See docs/agents/SPEC.md.
package grok

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const (
	pv = 1
	// pendingTimeout bounds how long a prompt that ends the file waits for
	// more chunks, and how quiet a session must be before usage.json is
	// trusted as the only usage source.
	pendingTimeout = 10 * time.Minute

	updatesFile = "updates.jsonl"
	usageFile   = "usage.json"
)

// now is replaced in tests.
var now = time.Now

type Source struct{}

// New returns the Grok CLI source.
func New() sources.Source { return &Source{} }

func (*Source) Name() string                   { return model.SourceGrokCLI }
func (*Source) Provider() string               { return model.ProviderXAI }
func (*Source) PV() int                        { return pv }
func (*Source) Prepare(env *sources.Env) error { return nil }

func sessionsDir(env *sources.Env) string { return filepath.Join(env.Home, ".grok", "sessions") }

// Files lists updates.jsonl and usage.json of every session directory,
// subagent forks included (they are sibling session directories).
func (*Source) Files(env *sources.Env) ([]string, error) {
	root := sessionsDir(env)
	cwds, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, cwd := range cwds {
		if !cwd.IsDir() {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(root, cwd.Name()))
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if !s.IsDir() {
				continue
			}
			dir := filepath.Join(root, cwd.Name(), s.Name())
			for _, name := range []string{updatesFile, usageFile} {
				if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
					out = append(out, filepath.Join(dir, name))
				}
			}
		}
	}
	return out, nil
}

func (*Source) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	if filepath.Base(path) == usageFile {
		return parseUsageJSON(env, path, cur)
	}
	return parseUpdates(env, path, cur)
}

func emitter(env *sources.Env) *jsonl.Emitter {
	return &jsonl.Emitter{Env: env, Provider: model.ProviderXAI, Source: model.SourceGrokCLI, PV: pv}
}

// session is what summary.json says about a session directory.
type session struct {
	ID        string
	Cwd       string
	Model     string
	Delegated bool // a subagent session: its "user" turns come from the parent agent
}

func loadSession(dir string) session {
	s := session{ID: filepath.Base(dir)}
	if cwd, err := url.PathUnescape(filepath.Base(filepath.Dir(dir))); err == nil {
		s.Cwd = cwd
	}
	b, err := jsonl.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return s
	}
	var sum struct {
		Info struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"info"`
		CurrentModelID  string `json:"current_model_id"`
		SessionKind     string `json:"session_kind"`
		ParentSessionID string `json:"parent_session_id"`
	}
	if !jsonl.Decode(b, &sum) {
		return s
	}
	if sum.Info.ID != "" {
		s.ID = sum.Info.ID
	}
	if sum.Info.Cwd != "" {
		s.Cwd = sum.Info.Cwd
	}
	s.Model = sum.CurrentModelID
	s.Delegated = sum.SessionKind != "" || sum.ParentSessionID != ""
	return s
}

// --- updates.jsonl ---

type counts struct {
	InputTokens         int64 `json:"inputTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	CachedReadTokens    int64 `json:"cachedReadTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
	ReasoningTokens     int64 `json:"reasoningTokens"`
	ModelCalls          int64 `json:"modelCalls"`
}

func (u counts) tokens() model.Tokens {
	return model.Tokens{
		In:        max(0, u.InputTokens-u.CachedReadTokens-u.CacheCreationTokens),
		CacheW:    u.CacheCreationTokens,
		CacheR:    u.CachedReadTokens,
		Out:       u.OutputTokens,
		Reasoning: u.ReasoningTokens,
		Calls:     u.ModelCalls,
	}
}

type updateLine struct {
	Timestamp float64 `json:"timestamp"`
	Params    struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Usage         *struct {
				counts
				ModelUsage map[string]counts `json:"modelUsage"`
			} `json:"usage"`
			Content *struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Meta struct {
					DisplayText *string `json:"displayText"`
				} `json:"_meta"`
			} `json:"content"`
			Meta struct {
				ModelID            string `json:"modelId"`
				HideFromScrollback bool   `json:"hideFromScrollback"`
			} `json:"_meta"`
		} `json:"update"`
		Meta struct {
			EventID          string `json:"eventId"`
			AgentTimestampMs int64  `json:"agentTimestampMs"`
		} `json:"_meta"`
	} `json:"params"`
}

// ts is the line's top-level timestamp (epoch seconds).
func (l *updateLine) ts() time.Time {
	sec, frac := math.Modf(l.Timestamp)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// promptTS prefers the agent's millisecond timestamp for user messages.
func (l *updateLine) promptTS() time.Time {
	if ms := l.Params.Meta.AgentTimestampMs; ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	return l.ts()
}

// prompt collects one user message: a run of consecutive
// user_message_chunk updates (text and image blocks).
type prompt struct {
	Key    string    `json:"key"` // eventId of the first chunk
	TS     time.Time `json:"ts"`
	Model  string    `json:"model,omitempty"`
	Parts  []string  `json:"parts,omitempty"`
	Image  bool      `json:"image,omitempty"`
	Hidden bool      `json:"hidden,omitempty"`
}

type carry struct {
	Model   string  `json:"model,omitempty"` // latest model named by a user message
	Pending *prompt `json:"pending,omitempty"`
}

var (
	keyTurnCompleted = []byte("turn_completed")
	keyUserChunk     = []byte("user_message_chunk")
)

func parseUpdates(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	var c carry
	if cur.Offset > 0 && len(cur.Carry) > 0 {
		_ = json.Unmarshal(cur.Carry, &c)
	}
	sess := loadSession(filepath.Dir(path))
	e := emitter(env)
	e.Dir = sess.Cwd
	r, err := jsonl.Open(path, cur.Offset)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer r.Close()

	flush := func() {
		p := c.Pending
		c.Pending = nil
		if p == nil || p.Hidden || sess.Delegated {
			return
		}
		text := strings.Join(p.Parts, "\n")
		if strings.TrimSpace(text) == "" {
			if !p.Image {
				return
			}
			text = "[image]"
		}
		// Reminders the CLI injects as user messages (e.g. after a goal update)
		// are not always hidden from the scrollback.
		if strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>") {
			return
		}
		e.Dir = sess.Cwd
		e.Activity(p.Key, p.TS, sess.ID, true)
		if e.Prompts() {
			pr := e.Prompt(p.Key, p.TS, sess.ID, sess.Cwd, text)
			pr.Model = p.Model
			e.Emit(pr)
		}
	}

	for {
		b, ok, err := r.Next()
		if err != nil {
			return sources.Batch{}, cur, err
		}
		if !ok {
			break
		}
		isChunk := bytes.Contains(b, keyUserChunk)
		if !isChunk && !bytes.Contains(b, keyTurnCompleted) {
			// Any other update ends the user message being collected.
			flush()
			continue
		}
		var l updateLine
		if !jsonl.Decode(b, &l) {
			flush()
			continue
		}
		u := &l.Params.Update
		if u.SessionUpdate != "user_message_chunk" {
			flush()
		}
		switch u.SessionUpdate {
		case "user_message_chunk":
			// A message has one text block, with its images after it. Messages
			// typed mid-turn are written back to back, so a second text block
			// starts the next message.
			if p := c.Pending; p != nil && len(p.Parts) > 0 && u.Content != nil && u.Content.Type == "text" && l.Params.Meta.EventID != "" {
				flush()
			}
			if c.Pending == nil {
				if l.Params.Meta.EventID == "" {
					continue
				}
				c.Pending = &prompt{Key: l.Params.Meta.EventID, TS: l.promptTS(), Model: u.Meta.ModelID}
			}
			p := c.Pending
			if u.Meta.ModelID != "" {
				c.Model = u.Meta.ModelID
				if p.Model == "" {
					p.Model = u.Meta.ModelID
				}
			}
			p.Hidden = p.Hidden || u.Meta.HideFromScrollback
			if ct := u.Content; ct != nil {
				switch ct.Type {
				case "text":
					// displayText is what the person typed; text may wrap it
					// with expanded attachments.
					if ct.Meta.DisplayText != nil {
						p.Parts = append(p.Parts, *ct.Meta.DisplayText)
					} else {
						p.Parts = append(p.Parts, ct.Text)
					}
				case "image":
					p.Image = true
				}
			}
		case "turn_completed":
			us := u.Usage
			id := l.Params.Meta.EventID
			if us == nil || id == "" {
				continue
			}
			ts := l.ts()
			if len(us.ModelUsage) == 0 {
				m := c.Model
				if m == "" {
					m = sess.Model
				}
				e.Usage(id+":"+m, ts, sess.ID, m, us.counts.tokens())
				continue
			}
			models := make([]string, 0, len(us.ModelUsage))
			for m := range us.ModelUsage {
				models = append(models, m)
			}
			sort.Strings(models)
			for _, m := range models {
				e.Usage(id+":"+m, ts, sess.ID, m, us.ModelUsage[m].tokens())
			}
		}
	}
	// A user message that ends the file may still be streaming chunks.
	if c.Pending != nil && now().Sub(c.Pending.TS) >= pendingTimeout {
		flush()
	}

	next := cur
	next.Offset = r.Offset()
	next.Carry = nil
	if c.Model != "" || c.Pending != nil {
		next.Carry, _ = json.Marshal(c)
	}
	return e.Batch, next, nil
}

// --- usage.json fallback ---

type usageJSON struct {
	SessionID string `json:"sessionId"`
	Turns     []struct {
		TurnNumber     int64  `json:"turnNumber"`
		EndedAt        string `json:"endedAt"`
		PrimaryModelID string `json:"primaryModelId"`
		counts
	} `json:"turns"`
}

// parseUsageJSON emits usage.json turns only for sessions whose
// updates.jsonl has no turn_completed usage. usage.json is rewritten in
// place, merges cancelled turns and can include child subagent usage, so
// it never supplements turn_completed data.
func parseUsageJSON(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	dir := filepath.Dir(path)
	next := cur
	next.Carry = nil
	updates := filepath.Join(dir, updatesFile)
	has, err := hasTurnCompleted(updates)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	if has {
		next.Offset = fileSize(path)
		return sources.Batch{}, next, nil
	}
	// A new session can write usage.json before its first turn_completed
	// line lands; decide once the session has been quiet for a while.
	for _, p := range []string{updates, path} {
		if st, err := os.Stat(p); err == nil && now().Sub(st.ModTime()) < pendingTimeout {
			next.Offset = 0
			return sources.Batch{}, next, nil
		}
	}
	b, err := jsonl.ReadFile(path)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	next.Offset = int64(len(b))
	var u usageJSON
	if !jsonl.Decode(b, &u) {
		return sources.Batch{}, next, nil
	}
	sess := loadSession(dir)
	if u.SessionID == "" {
		u.SessionID = sess.ID
	}
	e := emitter(env)
	e.Dir = sess.Cwd
	for _, t := range u.Turns {
		ts, ok := jsonl.ParseTime(t.EndedAt)
		if !ok {
			continue
		}
		m := t.PrimaryModelID
		if m == "" {
			m = sess.Model
		}
		key := u.SessionID + ":" + strconv.FormatInt(t.TurnNumber, 10) + ":" + t.EndedAt
		e.Usage(key, ts, u.SessionID, m, t.counts.tokens())
	}
	return e.Batch, next, nil
}

// hasTurnCompleted reports whether updates.jsonl holds at least one
// turn_completed update with usage. A missing file has none.
func hasTurnCompleted(path string) (bool, error) {
	r, err := jsonl.Open(path, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer r.Close()
	for {
		b, ok, err := r.Next()
		if err != nil || !ok {
			return false, err
		}
		if !bytes.Contains(b, keyTurnCompleted) {
			continue
		}
		var l updateLine
		if jsonl.Decode(b, &l) && l.Params.Update.SessionUpdate == "turn_completed" && l.Params.Update.Usage != nil {
			return true, nil
		}
	}
}

func fileSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}
