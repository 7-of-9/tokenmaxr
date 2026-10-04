// Package claude reads Claude Code transcripts (~/.claude/projects/**/*.jsonl)
// and prompt history (~/.claude/history.jsonl). See docs/agents/SPEC.md.
package claude

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const (
	// pv 2 added cacheW1h; the bump makes every transcript reparse.
	pv = 2
	// pendingTimeout is how long a prompt waits for the next assistant line
	// (which names its model) before it is sent with an empty model.
	pendingTimeout = 10 * time.Minute
	// recentKeys is how many in-flight usage keys the cursor carry keeps.
	// Streamed lines of one message are contiguous, so a few are plenty.
	recentKeys = 16
	// recentTTL is how long after its last usage line a file keeps those
	// keys: after that no more lines arrive for its in-flight messages, so a
	// settled file carries nothing (state.json holds a cursor per file).
	recentTTL = sources.CarryTTL
	synthetic = "<synthetic>"
)

// now is replaced in tests.
var now = time.Now

type Source struct {
	// transcripts holds every <sessionId>.jsonl basename under projects/,
	// so history.jsonl entries for those sessions are suppressed.
	transcripts map[string]bool
}

// New returns the Claude Code source.
func New() sources.Source { return &Source{} }

func (*Source) Name() string     { return model.SourceClaudeCode }
func (*Source) Provider() string { return model.ProviderAnthropic }
func (*Source) PV() int          { return pv }

func claudeDir(env *sources.Env) string { return filepath.Join(env.Home, ".claude") }

func historyPath(env *sources.Env) string { return filepath.Join(claudeDir(env), "history.jsonl") }

// transcriptFiles walks projects/ recursively: subagent and workflow
// transcripts nest several directories below their session.
func transcriptFiles(env *sources.Env) ([]string, error) {
	root := filepath.Join(claudeDir(env), "projects")
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return fs.SkipAll
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (s *Source) Prepare(env *sources.Env) error {
	files, err := transcriptFiles(env)
	if err != nil {
		return err
	}
	s.transcripts = make(map[string]bool, len(files))
	for _, f := range files {
		s.transcripts[strings.TrimSuffix(filepath.Base(f), ".jsonl")] = true
	}
	return nil
}

func (*Source) Files(env *sources.Env) ([]string, error) {
	files, err := transcriptFiles(env)
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
	return parseTranscript(env, path, cur)
}

func emitter(env *sources.Env) *jsonl.Emitter {
	return &jsonl.Emitter{Env: env, Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, PV: pv}
}

// --- transcripts ---

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	// CacheCreation splits cache_creation_input_tokens by TTL.
	CacheCreation *struct {
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokens        int64 `json:"output_tokens"`
	OutputTokensDetails *struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	// iterations[] is deliberately not decoded: it repeats the same tokens.
}

type line struct {
	Type             string `json:"type"`
	UUID             string `json:"uuid"`
	SessionID        string `json:"sessionId"`
	Timestamp        string `json:"timestamp"`
	Cwd              string `json:"cwd"`
	RequestID        string `json:"requestId"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	PromptSource     string `json:"promptSource"`
	Origin           *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *usage          `json:"usage"`
	} `json:"message"`
	// Attachment is set on type=attachment lines. A queued_command attachment
	// is a prompt typed while a turn was running; Claude Code hands it to the
	// model inside that turn and writes no user line for it.
	Attachment *struct {
		Type string `json:"type"`
		// OrganizationUUID is set on credential_org attachments: the org of
		// the credential in use from this line on (SPEC "Accounts").
		OrganizationUUID string          `json:"organizationUuid"`
		Prompt           json.RawMessage `json:"prompt"`
		CommandMode      string          `json:"commandMode"`
		IsMeta           bool            `json:"isMeta"`
		Origin           *struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	} `json:"attachment"`
}

// acc is the running fieldwise max for one usage key.
type acc struct {
	Key     string       `json:"key"`
	TS      time.Time    `json:"ts"`
	Session string       `json:"session"`
	Model   string       `json:"model"`
	Tokens  model.Tokens `json:"tokens"`
	// Cwd is the folder of the usage line, the same path a prompt stores.
	Cwd string `json:"cwd,omitempty"`
	// Hint is the file's credential hint when the key was first seen.
	Hint sources.Hint `json:"hint,omitzero"`
}

type carry struct {
	// Recent holds the last usage keys seen, oldest first, so a message
	// whose streamed lines span two reads is still emitted at its max.
	Recent []acc `json:"recent,omitempty"`
	// Last is the newest usage line timestamp behind Recent.
	Last time.Time `json:"last,omitzero"`
	// Pending holds prompts still waiting for the next assistant model.
	Pending []model.PromptRecord `json:"pending,omitempty"`
	// Hint is the latest credential_org of this file (hashed), so a read
	// that resumes after that line still attributes by it.
	Hint sources.Hint `json:"hint,omitzero"`
}

var (
	keyUsage         = []byte(`"usage"`)
	keyUser          = []byte(`"user"`)
	keyToolUseResult = []byte(`"toolUseResult"`)
	keyQueued        = []byte(`"queued_command"`)
	keyCredOrg       = []byte(`"credential_org"`)
)

func parseTranscript(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	var c carry
	if cur.Offset > 0 && len(cur.Carry) > 0 {
		_ = json.Unmarshal(cur.Carry, &c) // a bad carry only loses the optimisation
	}
	e := emitter(env)
	e.Hint = c.Hint
	r, err := jsonl.Open(path, cur.Offset)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer r.Close()

	recent := make(map[string]*acc, len(c.Recent))
	var lastKeys []string // keys in last-seen order, carried ones first
	for i := range c.Recent {
		a := c.Recent[i]
		recent[a.Key] = &a
		lastKeys = append(lastKeys, a.Key)
	}
	var order []*acc // keys touched in this read, in first-seen order
	touched := map[string]bool{}

	for {
		b, ok, err := r.Next()
		if err != nil {
			return sources.Batch{}, cur, err
		}
		if !ok {
			break
		}
		isUsage := bytes.Contains(b, keyUsage)
		isUser := bytes.Contains(b, keyUser) && !bytes.Contains(b, keyToolUseResult)
		if !isUsage && !isUser && !bytes.Contains(b, keyQueued) && !bytes.Contains(b, keyCredOrg) {
			continue
		}
		var l line
		if !jsonl.Decode(b, &l) {
			continue
		}
		ts, okTS := jsonl.ParseTime(l.Timestamp)
		switch l.Type {
		case "assistant":
			if l.Message.Usage == nil || l.Message.Model == synthetic || !okTS {
				continue
			}
			m := l.Message.Model
			if m != "" {
				for _, p := range c.Pending {
					p.Model = m
					e.Emit(p)
				}
				c.Pending = c.Pending[:0]
			}
			key := l.Message.ID
			if key == "" {
				key = l.RequestID
			}
			if key == "" {
				key = l.SessionID + ":" + l.UUID
			}
			u := l.Message.Usage
			t := model.Tokens{
				In:     u.InputTokens,
				CacheW: u.CacheCreationInputTokens,
				CacheR: u.CacheReadInputTokens,
				Out:    u.OutputTokens,
				Calls:  1,
			}
			if d := u.OutputTokensDetails; d != nil {
				t.Reasoning = d.ThinkingTokens
			}
			if cc := u.CacheCreation; cc != nil {
				t.CacheW1h = cc.Ephemeral1h
			}
			a := recent[key]
			if a == nil {
				a = &acc{Key: key, TS: ts, Session: l.SessionID, Model: m, Cwd: l.Cwd, Hint: e.Hint}
				recent[key] = a
			}
			if a.Cwd == "" {
				a.Cwd = l.Cwd
			}
			a.Tokens.Max(t)
			if ts.Before(a.TS) {
				a.TS = ts
			}
			if a.Model == "" {
				a.Model = m
			}
			if !touched[key] {
				touched[key] = true
				order = append(order, a)
			}
			lastKeys = append(lastKeys, key)
			if ts.After(c.Last) {
				c.Last = ts
			}
		case "user":
			if !okTS || l.IsMeta || l.IsSidechain || l.IsCompactSummary {
				continue
			}
			text, ok := promptText(&l)
			if !ok {
				continue
			}
			e.Dir = l.Cwd
			e.Activity(l.UUID, ts, l.SessionID, true)
			if e.Prompts() {
				c.Pending = append(c.Pending, e.Prompt(l.UUID, ts, l.SessionID, l.Cwd, text))
			}
		case "attachment":
			if a := l.Attachment; a != nil && a.Type == "credential_org" {
				if okTS && a.OrganizationUUID != "" {
					c.Hint = jsonl.HintFor(env, model.ProviderAnthropic, sources.HintOrg, a.OrganizationUUID, ts)
					e.Hint = c.Hint
				}
				continue
			}
			if !okTS || l.IsSidechain || l.UUID == "" {
				continue
			}
			text, ok := queuedText(&l)
			if !ok {
				continue
			}
			e.Dir = l.Cwd
			e.Activity(l.UUID, ts, l.SessionID, true)
			if e.Prompts() {
				c.Pending = append(c.Pending, e.Prompt(l.UUID, ts, l.SessionID, l.Cwd, text))
			}
		}
	}

	for _, a := range order {
		e.Hint = a.Hint
		e.Dir = a.Cwd
		e.Usage(a.Key, a.TS, a.Session, a.Model, a.Tokens)
	}
	e.Hint = c.Hint

	// Prompts with no assistant reply after pendingTimeout go out without a model.
	keep := c.Pending[:0]
	for _, p := range c.Pending {
		if now().Sub(p.TS) >= pendingTimeout {
			e.Emit(p)
		} else {
			keep = append(keep, p)
		}
	}
	c.Pending = keep

	c.Recent = c.Recent[:0]
	if now().Sub(c.Last) < recentTTL {
		seen := map[string]bool{}
		for i := len(lastKeys) - 1; i >= 0 && len(seen) < recentKeys; i-- {
			if k := lastKeys[i]; !seen[k] {
				seen[k] = true
				c.Recent = append(c.Recent, *recent[k])
			}
		}
		slices.Reverse(c.Recent)
	} else {
		c.Last = time.Time{}
	}

	next := cur
	next.Offset = r.Offset()
	next.Carry = marshalCarry(c)
	return e.Batch, next, nil
}

func marshalCarry(c carry) json.RawMessage {
	if len(c.Recent) == 0 && len(c.Pending) == 0 && c.Hint.IsZero() {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return b
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentText joins the text blocks of a message content (a string or an
// array of blocks). ok is false for tool results and unknown shapes.
func contentText(raw json.RawMessage) (text string, image, ok bool) {
	switch {
	case len(raw) == 0:
		return "", false, false
	case raw[0] == '"':
		if !jsonl.Decode(raw, &text) {
			return "", false, false
		}
		return text, false, true
	case raw[0] == '[':
		var blocks []block
		if !jsonl.Decode(raw, &blocks) {
			return "", false, false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return "", false, false
			case "text":
				parts = append(parts, b.Text)
			case "image":
				image = true
			}
		}
		return strings.Join(parts, "\n"), image, true
	}
	return "", false, false
}

// promptText returns the text a person typed, or false for tool results,
// command wrappers, system reminders and interruption markers.
func promptText(l *line) (string, bool) {
	text, image, ok := contentText(l.Message.Content)
	if !ok {
		return "", false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		if image {
			return "[image]", true
		}
		return "", false
	}
	if strings.HasPrefix(t, "[Request interrupted by user") {
		return "", false
	}
	// Newer transcripts say who wrote the line; task notifications and
	// other system-injected turns are not prompts.
	if l.Origin != nil && l.Origin.Kind != "" && l.Origin.Kind != "human" {
		return "", false
	}
	// Command wrappers and reminders start with a tag. A typed prompt that
	// happens to start with one (pasted markup) is still a prompt.
	if strings.HasPrefix(t, "<") && l.PromptSource != "typed" {
		return "", false
	}
	return text, true
}

// queuedText returns the prompt of a queued_command attachment that a person
// typed mid-turn. Task notifications, coordinator and peer messages use the
// same attachment type and are not prompts.
func queuedText(l *line) (string, bool) {
	a := l.Attachment
	if a == nil || a.Type != "queued_command" || a.IsMeta || a.CommandMode != "prompt" ||
		a.Origin == nil || a.Origin.Kind != "human" {
		return "", false
	}
	text, image, ok := contentText(a.Prompt)
	if !ok {
		return "", false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		if image {
			return "[image]", true
		}
		return "", false
	}
	return text, true
}

// --- history.jsonl ---

type pasted struct {
	Type        string `json:"type"`
	Content     string `json:"content"`
	ContentHash string `json:"contentHash"`
}

type histLine struct {
	Display        string            `json:"display"`
	PastedContents map[string]pasted `json:"pastedContents"`
	Timestamp      int64             `json:"timestamp"`
	Project        string            `json:"project"`
	SessionID      string            `json:"sessionId"`
}

var (
	pastePlaceholder = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)
	slashCommand     = regexp.MustCompile(`^/[A-Za-z0-9:_-]+(\s|$)`)
)

// parseHistory emits prompts and activity for history entries whose session
// has no transcript (for example, transcripts deleted by cleanupPeriodDays).
func (s *Source) parseHistory(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	if s.transcripts == nil {
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
		if !jsonl.Decode(b, &h) || h.Timestamp == 0 {
			next.Offset = r.Offset()
			continue
		}
		if s.transcripts[h.SessionID] && h.SessionID != "" {
			next.Offset = r.Offset()
			continue
		}
		ts := time.UnixMilli(h.Timestamp).UTC()
		// A brand-new session's transcript may not have existed when
		// Prepare ran; leave recent entries for a later scan to decide.
		if now().Sub(ts) < pendingTimeout {
			next.Offset = start
			break
		}
		next.Offset = r.Offset()
		text := expandPastes(env, h)
		t := strings.TrimSpace(text)
		if t == "" || strings.HasPrefix(t, "<") || slashCommand.MatchString(t) {
			continue
		}
		key := "hist:" + h.SessionID + ":" + strconv.FormatInt(h.Timestamp, 10)
		e.Dir = h.Project
		e.Activity(key, ts, h.SessionID, false)
		if e.Prompts() {
			e.Emit(e.Prompt(key, ts, h.SessionID, h.Project, text))
		}
	}
	return e.Batch, next, nil
}

// expandPastes replaces "[Pasted text #N]" placeholders with the pasted text
// when history.jsonl or ~/.claude/paste-cache still has it.
func expandPastes(env *sources.Env, h histLine) string {
	if len(h.PastedContents) == 0 {
		return h.Display
	}
	return pastePlaceholder.ReplaceAllStringFunc(h.Display, func(m string) string {
		id := pastePlaceholder.FindStringSubmatch(m)[1]
		p, ok := h.PastedContents[id]
		if !ok || p.Type != "text" {
			return m
		}
		if p.Content != "" {
			return p.Content
		}
		if p.ContentHash != "" && !strings.ContainsAny(p.ContentHash, `/\.`) {
			if b, err := jsonl.ReadFile(filepath.Join(claudeDir(env), "paste-cache", p.ContentHash+".txt")); err == nil {
				return string(b)
			}
		}
		return m
	})
}
