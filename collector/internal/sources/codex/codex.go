// Package codex reads Codex CLI rollouts ($CODEX_HOME or ~/.codex:
// sessions/**/rollout-*.jsonl, archived_sessions/**) and history.jsonl.
// See docs/agents/SPEC.md.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const (
	pv = 1
	// pendingTimeout bounds how long a prompt waits for its turn's
	// turn_context, and how old a history-only entry must be before it is
	// trusted to have no rollout.
	pendingTimeout = 10 * time.Minute
)

// now is replaced in tests.
var now = time.Now

var rolloutName = regexp.MustCompile(`^rollout-.*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

type Source struct {
	// rollouts holds the thread ids that have a rollout file, so
	// history.jsonl entries for them are suppressed.
	rollouts map[string]bool
}

// New returns the Codex source.
func New() sources.Source { return &Source{} }

func (*Source) Name() string     { return model.SourceCodex }
func (*Source) Provider() string { return model.ProviderOpenAI }
func (*Source) PV() int          { return pv }

func codexDir(env *sources.Env) string {
	if env.CodexHome != "" {
		return env.CodexHome
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(env.Home, ".codex")
}

func historyPath(env *sources.Env) string { return filepath.Join(codexDir(env), "history.jsonl") }

func rolloutFiles(env *sources.Env) ([]string, error) { return RolloutFiles(codexDir(env)), nil }

// RolloutFiles lists the rollout files under a Codex directory's sessions
// and archived_sessions folders. Symbolic links (and Windows junctions) to
// folders are followed, those folders themselves included, and each real
// folder is read once (fsx.WalkFollow), so a link loop cannot hang the scan.
func RolloutFiles(dir string) []string {
	var out []string
	roots := []string{filepath.Join(dir, "sessions"), filepath.Join(dir, "archived_sessions")}
	fsx.WalkFollow(roots, func(p string, d fs.DirEntry, err error) error {
		// A missing folder or an unreadable entry: there is nothing to list there.
		if err == nil && !d.IsDir() && rolloutName.MatchString(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (s *Source) Prepare(env *sources.Env) error {
	files, err := rolloutFiles(env)
	if err != nil {
		return err
	}
	s.rollouts = make(map[string]bool, len(files))
	for _, f := range files {
		if m := rolloutName.FindStringSubmatch(filepath.Base(f)); m != nil {
			s.rollouts[m[1]] = true
		}
	}
	return nil
}

func (*Source) Files(env *sources.Env) ([]string, error) {
	files, err := rolloutFiles(env)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(historyPath(env)); err == nil {
		files = append(files, historyPath(env))
	}
	return files, nil
}

func (s *Source) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	if filepath.Clean(path) == filepath.Clean(historyPath(env)) {
		return s.parseHistory(env, path, cur)
	}
	return parseRollout(env, path, cur)
}

func emitter(env *sources.Env) *jsonl.Emitter {
	return &jsonl.Emitter{Env: env, Provider: model.ProviderOpenAI, Source: model.SourceCodex, PV: pv}
}

// --- rollouts ---

type line struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *int64          `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Metadata  struct {
		InheritedUserMessage bool `json:"inherited_user_message"`
	} `json:"metadata"`
}

type sessionMeta struct {
	ID           string          `json:"id"`
	Cwd          string          `json:"cwd"`
	Source       json.RawMessage `json:"source"`
	ThreadSource string          `json:"thread_source"`
	// CreatorAccountID names the account that created the thread (newer
	// Codex builds); it is hashed at once into the rollout's hint.
	CreatorAccountID string `json:"creator_account_id"`
}

type turnContext struct {
	Model  string `json:"model"`
	Cwd    string `json:"cwd"`
	TurnID string `json:"turn_id"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (u tokenUsage) tokens() model.Tokens {
	return model.Tokens{
		In:        max(0, u.InputTokens-u.CachedInputTokens-u.CacheWriteInputTokens),
		CacheW:    u.CacheWriteInputTokens,
		CacheR:    u.CachedInputTokens,
		Out:       u.OutputTokens,
		Reasoning: u.ReasoningOutputTokens,
		Calls:     1,
	}
}

// payload covers the fields of event_msg, response_item and
// token_usage_record payloads that the parser reads.
type payload struct {
	Type   string `json:"type"`
	TurnID string `json:"turn_id"`
	// event_msg / token_count
	Info *struct {
		TotalTokenUsage *tokenUsage `json:"total_token_usage"`
		LastTokenUsage  *tokenUsage `json:"last_token_usage"`
	} `json:"info"`
	// response_item / message
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// token_usage_record
	ResponseID string      `json:"response_id"`
	Usage      *tokenUsage `json:"usage"`
}

type pending struct {
	Prompt model.PromptRecord `json:"prompt"`
	// Turn is the task_started turn the prompt belongs to; the prompt takes
	// that turn's turn_context model.
	Turn string `json:"turn"`
}

type carry struct {
	Meta      bool      `json:"meta,omitempty"` // first session_meta seen
	Rollout   string    `json:"rollout,omitempty"`
	Subagent  bool      `json:"subagent,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Model     string    `json:"model,omitempty"`
	ModelTurn string    `json:"modelTurn,omitempty"` // turn_id of the latest turn_context
	Turn      string    `json:"turn,omitempty"`      // turn_id of the latest task_started
	HasTotal  bool      `json:"hasTotal,omitempty"`
	LastTotal int64     `json:"lastTotal,omitempty"`
	Epoch     int       `json:"epoch,omitempty"`
	SawRecord bool      `json:"sawRecord,omitempty"`
	Lines     int64     `json:"lines,omitempty"` // complete lines consumed, for the ordinal fallback
	Pending   []pending `json:"pending,omitempty"`
	// Hint is the rollout's creator account (hashed), when Codex wrote one.
	Hint sources.Hint `json:"hint,omitzero"`
}

var prefilter = [][]byte{
	[]byte("session_meta"), []byte("turn_context"), []byte("task_started"),
	[]byte("token_count"), []byte("token_usage_record"), []byte(`"user"`),
}

func interesting(b []byte) bool {
	for _, p := range prefilter {
		if bytes.Contains(b, p) {
			return true
		}
	}
	return false
}

func parseRollout(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	var c carry
	if cur.Offset > 0 && len(cur.Carry) > 0 {
		_ = json.Unmarshal(cur.Carry, &c)
	}
	e := emitter(env)
	e.Hint = c.Hint
	r, err := jsonl.Open(path, cur.Offset)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer r.Close()

	rollout := func() string {
		if c.Rollout == "" {
			if m := rolloutName.FindStringSubmatch(filepath.Base(path)); m != nil {
				return m[1]
			}
		}
		return c.Rollout
	}
	emitPending := func(p pending, m string) {
		p.Prompt.Model = m
		e.Emit(p.Prompt)
	}

	for {
		b, ok, err := r.Next()
		if err != nil {
			return sources.Batch{}, cur, err
		}
		if !ok {
			break
		}
		index := c.Lines
		c.Lines++
		if !interesting(b) {
			continue
		}
		var l line
		if !jsonl.Decode(b, &l) {
			continue
		}
		ts, okTS := jsonl.ParseTime(l.Timestamp)
		switch l.Type {
		case "session_meta":
			var m sessionMeta
			if !jsonl.Decode(l.Payload, &m) || c.Meta {
				continue
			}
			// The first session_meta is this thread's; a subagent thread
			// repeats its parent's after it.
			c.Meta = true
			c.Rollout = m.ID
			c.Cwd = m.Cwd
			c.Subagent = m.ThreadSource == "subagent" || isSubagentSource(m.Source)
			if m.CreatorAccountID != "" && okTS {
				c.Hint = jsonl.HintFor(env, model.ProviderOpenAI, sources.HintAccount, m.CreatorAccountID, ts)
				e.Hint = c.Hint
			}
		case "turn_context":
			var t turnContext
			if !jsonl.Decode(l.Payload, &t) {
				continue
			}
			prev := c.Model
			if t.Model != "" {
				c.Model = t.Model
			}
			if t.Cwd != "" {
				c.Cwd = t.Cwd
			}
			c.ModelTurn = t.TurnID
			// Waiting prompts belong to this turn, or to an earlier turn
			// whose own turn_context never came (that keeps the old model).
			for _, p := range c.Pending {
				if p.Turn == "" || p.Turn == t.TurnID || prev == "" {
					emitPending(p, c.Model)
				} else {
					emitPending(p, prev)
				}
			}
			c.Pending = c.Pending[:0]
		case "token_usage_record":
			c.SawRecord = true
			var p payload
			if !okTS || !jsonl.Decode(l.Payload, &p) || p.Usage == nil || p.ResponseID == "" {
				continue
			}
			e.Dir = c.Cwd
			e.Usage(p.ResponseID, ts, rollout(), c.Model, p.Usage.tokens())
		case "event_msg":
			var p payload
			if !jsonl.Decode(l.Payload, &p) {
				continue
			}
			switch p.Type {
			case "task_started":
				// A new turn: prompts of the previous turn that never saw
				// their turn_context take the current model. Prompts written
				// before any turn started (no model known yet) join this turn.
				keep := c.Pending[:0]
				for _, q := range c.Pending {
					if q.Turn == "" && c.Model == "" {
						q.Turn = p.TurnID
						keep = append(keep, q)
					} else {
						emitPending(q, c.Model)
					}
				}
				c.Pending = keep
				c.Turn = p.TurnID
			case "token_count":
				// Mode rule: after a token_usage_record line, legacy
				// token_count lines in the same file are ignored.
				if c.SawRecord || !okTS || p.Info == nil || p.Info.TotalTokenUsage == nil {
					continue
				}
				total := p.Info.TotalTokenUsage.TotalTokens
				if c.HasTotal && total == c.LastTotal {
					continue
				}
				if c.HasTotal && total < c.LastTotal {
					c.Epoch++
				}
				c.HasTotal, c.LastTotal = true, total
				if p.Info.LastTokenUsage == nil {
					continue
				}
				key := rollout() + ":" + strconv.Itoa(c.Epoch) + ":" + strconv.FormatInt(total, 10)
				e.Dir = c.Cwd
				e.Usage(key, ts, rollout(), c.Model, p.Info.LastTokenUsage.tokens())
			}
		case "response_item":
			var p payload
			if !okTS || !jsonl.Decode(l.Payload, &p) || p.Type != "message" || p.Role != "user" {
				continue
			}
			// Subagent threads get their "user" turns from the parent agent,
			// and forks replay the parent's prompts.
			if c.Subagent || l.Metadata.InheritedUserMessage {
				continue
			}
			text, ok := promptText(p)
			if !ok {
				continue
			}
			ord := index
			if l.Ordinal != nil {
				ord = *l.Ordinal
			}
			key := rollout() + ":" + strconv.FormatInt(ord, 10)
			e.Dir = c.Cwd
			e.Activity(key, ts, rollout(), true)
			if !e.Prompts() {
				continue
			}
			pr := e.Prompt(key, ts, rollout(), c.Cwd, text)
			// turn_context usually precedes the user message in its turn,
			// but sometimes follows it; wait for it in that case.
			if (c.Turn == "" && c.Model != "") || (c.Turn != "" && c.ModelTurn == c.Turn) {
				pr.Model = c.Model
				e.Emit(pr)
			} else {
				c.Pending = append(c.Pending, pending{Prompt: pr, Turn: c.Turn})
			}
		}
	}

	keep := c.Pending[:0]
	for _, p := range c.Pending {
		if now().Sub(p.Prompt.TS) >= pendingTimeout {
			emitPending(p, c.Model)
		} else {
			keep = append(keep, p)
		}
	}
	c.Pending = keep

	next := cur
	next.Offset = r.Offset()
	next.Carry, _ = json.Marshal(c)
	return e.Batch, next, nil
}

// isSubagentSource reports whether session_meta.source is the object form
// Codex writes for spawned subagent threads ({"subagent": ...}).
func isSubagentSource(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	var m map[string]json.RawMessage
	if !jsonl.Decode(raw, &m) {
		return false
	}
	_, ok := m["subagent"]
	return ok
}

// imageTag matches the text blocks Codex writes around an image a person
// attached: `<image name=[Image #1]>` before the input_image, `</image>` after.
var imageTag = regexp.MustCompile(`^</?image\b[^>]*>$`)

func promptText(p payload) (string, bool) {
	var parts []string
	image, wrapped, texts := false, false, false
	for _, b := range p.Content {
		switch b.Type {
		case "input_text", "text":
			texts = true
			if imageTag.MatchString(strings.TrimSpace(b.Text)) {
				wrapped = true
				continue
			}
			parts = append(parts, b.Text)
		case "input_image":
			image = true
		}
	}
	text := strings.Join(parts, "\n")
	t := strings.TrimSpace(text)
	if t == "" {
		// An attached image comes wrapped in <image> text blocks. A bare
		// input_image with no text block at all is a tool's output (for
		// example view_image), not something a person typed.
		if image && (wrapped || texts) {
			return "[image]", true
		}
		return "", false
	}
	// Environment context, AGENTS.md and other injected instructions.
	if strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS") {
		return "", false
	}
	return text, true
}

// --- history.jsonl ---

// typed reports whether a history entry is something a person typed, not
// injected context (environment, AGENTS.md and other instructions).
func typed(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && !strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "# AGENTS")
}

type histLine struct {
	SessionID string `json:"session_id"`
	TS        int64  `json:"ts"`
	Text      string `json:"text"`
}

// parseHistory emits prompts and activity for history entries whose thread
// has no rollout file (rollouts deleted or never written).
func (s *Source) parseHistory(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	if s.rollouts == nil {
		if err := s.Prepare(env); err != nil {
			return sources.Batch{}, cur, err
		}
	}
	e := emitter(env)
	r, err := jsonl.Open(path, cur.Offset)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer r.Close()
	next := cur
	next.Carry = nil
	for {
		start := r.Offset()
		b, ok, err := r.Next()
		if err != nil {
			return sources.Batch{}, cur, err
		}
		if !ok {
			break
		}
		var h histLine
		if !jsonl.Decode(b, &h) || h.TS == 0 || (h.SessionID != "" && s.rollouts[h.SessionID]) {
			next.Offset = r.Offset()
			continue
		}
		ts := time.Unix(h.TS, 0).UTC()
		// A new thread's rollout may not have existed when Prepare ran.
		if now().Sub(ts) < pendingTimeout {
			next.Offset = start
			break
		}
		next.Offset = r.Offset()
		if !typed(h.Text) {
			continue
		}
		key := "hist:" + h.SessionID + ":" + strconv.FormatInt(h.TS, 10)
		e.Activity(key, ts, h.SessionID, false)
		if e.Prompts() {
			e.Emit(e.Prompt(key, ts, h.SessionID, "", h.Text))
		}
	}
	return e.Batch, next, nil
}

// --- missing logs ---

// Missing counts the Codex sessions that history.jsonl records but that have
// no rollout file: deleted since, or made on another machine and synced
// here. Their prompts are counted (as activity) but their tokens are not,
// unless account history (internal/accountusage) covers them.
type Missing struct {
	Sessions int `json:"sessions"`
	Prompts  int `json:"prompts"`
	// History is the number of history.jsonl prompts checked (0: no history).
	History int `json:"history"`
}

// MissingLogs reads the history.jsonl of every Codex directory in dirs
// against the rollout files of all of them: one machine's Codex directories
// (CODEX_HOME and ~/.codex) can hold different parts of the same history, and
// a rollout read from either is a log on this machine. A prompt listed in
// two histories (a copied history.jsonl) counts once. Entries from the last
// pendingTimeout are left out: a new session's rollout may not be written
// yet. Directories without history.jsonl have nothing missing; the first
// unreadable history is returned with the counts of the others.
func MissingLogs(at time.Time, dirs ...string) (Missing, error) {
	var m Missing
	have := map[string]bool{}
	for _, dir := range dirs {
		for _, f := range RolloutFiles(dir) {
			if g := rolloutName.FindStringSubmatch(filepath.Base(f)); g != nil {
				have[g[1]] = true
			}
		}
	}
	sessions := map[string]bool{}
	prompts := map[string]bool{}
	var first error
	for _, dir := range dirs {
		if err := missingIn(dir, at, have, sessions, prompts, &m); err != nil && first == nil {
			first = err
		}
	}
	return m, first
}

func missingIn(dir string, at time.Time, have, sessions, prompts map[string]bool, m *Missing) error {
	r, err := jsonl.Open(filepath.Join(dir, "history.jsonl"), 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer r.Close()
	for {
		b, ok, err := r.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		var h histLine
		if !jsonl.Decode(b, &h) || h.TS == 0 || h.SessionID == "" || at.Sub(time.Unix(h.TS, 0)) < pendingTimeout || !typed(h.Text) {
			continue
		}
		key := h.SessionID + ":" + strconv.FormatInt(h.TS, 10)
		if prompts[key] {
			continue
		}
		prompts[key] = true
		m.History++
		if have[h.SessionID] {
			continue
		}
		m.Prompts++
		if !sessions[h.SessionID] {
			sessions[h.SessionID] = true
			m.Sessions++
		}
	}
}

// Text is the one-line report status, doctor and scan --dry-run print.
func (m Missing) Text() string {
	if m.Sessions == 0 {
		return "every Codex session in history.jsonl has its log on this machine"
	}
	s := "s have"
	if m.Sessions == 1 {
		s = " has"
	}
	p := "s"
	if m.Prompts == 1 {
		p = ""
	}
	return strconv.Itoa(m.Sessions) + " Codex session" + s + " no log on this machine (deleted or made elsewhere): " +
		strconv.Itoa(m.Prompts) + " prompt" + p + " without token records"
}
