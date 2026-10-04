// Package gemini reads Gemini CLI chat sessions
// (~/.gemini/tmp/<projectHash>/chats/session-*.json): one usage event per
// gemini message that carries tokens, one activity and prompt per user
// message. See docs/agents/SPEC.md "Gemini CLI".
//
// Each session is one JSON document that Gemini rewrites as it grows, so
// this is a whole-file source: the file is re-read from 0 whenever its size
// or mtime changes and the content-derived ids keep that idempotent.
package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const (
	pv = 1

	// ProviderName and SourceName are the wire ids (SPEC "Providers").
	ProviderName = "google"
	SourceName   = "gemini-cli"

	accountsFile = "google_accounts.json"
)

type Source struct{}

// New returns the Gemini CLI source.
func New() sources.Source { return &Source{} }

func (*Source) Name() string                   { return SourceName }
func (*Source) Provider() string               { return ProviderName }
func (*Source) PV() int                        { return pv }
func (*Source) Prepare(env *sources.Env) error { return nil }

// Dir is the Gemini CLI directory of a home.
func Dir(home string) string { return filepath.Join(home, ".gemini") }

// Files lists every tmp/<projectHash>/chats/session-*.json.
func (*Source) Files(env *sources.Env) ([]string, error) {
	tmp := filepath.Join(Dir(env.Home), "tmp")
	projects, err := os.ReadDir(tmp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		chats := filepath.Join(tmp, p.Name(), "chats")
		ents, err := os.ReadDir(chats)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if e.Type().IsRegular() && strings.HasPrefix(n, "session-") && strings.HasSuffix(n, ".json") {
				out = append(out, filepath.Join(chats, n))
			}
		}
	}
	return out, nil
}

// --- session shape ---

type tokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
	Total    int64 `json:"total"`
}

func (t tokens) any() bool {
	return t.Input > 0 || t.Output > 0 || t.Cached > 0 || t.Thoughts > 0 || t.Tool > 0
}

// tokens maps Gemini's buckets to the disjoint wire buckets: input includes
// the cached part, output excludes thoughts (verified on real sessions:
// total == input + output + thoughts + tool and cached <= input).
func (t tokens) tokens() model.Tokens {
	return model.Tokens{
		In:        max(0, t.Input-t.Cached) + t.Tool,
		CacheR:    t.Cached,
		Out:       t.Output + t.Thoughts,
		Reasoning: t.Thoughts,
		Calls:     1,
	}
}

type message struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Tokens    *tokens         `json:"tokens"`
	Model     string          `json:"model"`
}

type session struct {
	SessionID   string    `json:"sessionId"`
	ProjectHash string    `json:"projectHash"`
	Messages    []message `json:"messages"`
}

// text returns a message's text: a string, or the text parts of a list.
func (m *message) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// Parse re-reads the whole session (whole-file source) and sets the
// cursor's Offset to the file size.
func (*Source) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	data, err := jsonl.ReadFile(path)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	next := cur
	next.Carry = nil
	next.Offset = int64(len(data))
	var s session
	if !jsonl.Decode(data, &s) {
		// Being rewritten (or truncated): the next size/mtime change
		// re-offers the file.
		return sources.Batch{}, next, nil
	}
	if s.SessionID == "" {
		s.SessionID = sessionIDFromName(path)
	}
	e := &jsonl.Emitter{Env: env, Provider: ProviderName, Source: SourceName, PV: pv}
	hasUsage := false
	for _, m := range s.Messages {
		if m.Type == "gemini" && m.Tokens != nil && m.Tokens.any() {
			hasUsage = true
			break
		}
	}
	e.Dir = workspaceFor(env.Home, s.ProjectHash, path)
	for i, m := range s.Messages {
		ts, ok := jsonl.ParseTime(m.Timestamp)
		if !ok || m.ID == "" {
			continue
		}
		key := s.SessionID + ":" + m.ID
		switch m.Type {
		case "gemini":
			if m.Tokens == nil || !m.Tokens.any() {
				continue
			}
			e.Usage(key, ts, s.SessionID, m.Model, m.Tokens.tokens())
		case "user":
			text := m.text()
			if strings.TrimSpace(text) == "" {
				continue
			}
			e.Activity(key, ts, s.SessionID, hasUsage)
			if !e.Prompts() {
				continue
			}
			p := e.Prompt(key, ts, s.SessionID, e.Dir, text)
			p.Model = nextModel(s.Messages, i)
			e.Emit(p)
		}
	}
	return e.Batch, next, nil
}

// nextModel is the model of the next gemini message after index i, or "".
func nextModel(ms []message, i int) string {
	for _, m := range ms[i+1:] {
		if m.Type == "gemini" {
			return m.Model
		}
	}
	return ""
}

// sessionIDFromName recovers the id part of session-<date>-<id>.json when
// the document lacks sessionId.
func sessionIDFromName(path string) string {
	n := strings.TrimSuffix(filepath.Base(path), ".json")
	if i := strings.LastIndexByte(n, '-'); i >= 0 {
		return n[i+1:]
	}
	return n
}

// workspaceFor is the project folder when ~/.gemini/projects.json maps one
// to this projectHash (newer Gemini CLI builds keep {"projects": {path:
// hash}}), else the hash from the path or the document.
func workspaceFor(home, hash, path string) string {
	if hash == "" {
		hash = filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	if b, err := os.ReadFile(filepath.Join(Dir(home), "projects.json")); err == nil {
		var p struct {
			Projects map[string]string `json:"projects"`
		}
		if json.Unmarshal(b, &p) == nil {
			for folder, h := range p.Projects {
				if h == hash && folder != "" {
					return folder
				}
			}
		}
	}
	return hash
}

// Account reads the signed-in account from google_accounts.json (field
// "active"). The value is an account id or email: it is hashed by the core,
// never stored, and doubles as the private label. oauth_creds.json is never
// opened.
func Account(home string) (nativeID, label string, ok bool) {
	b, err := os.ReadFile(filepath.Join(Dir(home), accountsFile))
	if err != nil {
		return "", "", false
	}
	var f struct {
		Active string `json:"active"`
	}
	if json.Unmarshal(b, &f) != nil || strings.TrimSpace(f.Active) == "" {
		return "", "", false
	}
	a := strings.TrimSpace(f.Active)
	return a, a, true
}
