// Package cursor reads the Cursor IDE's global state database
// (User/globalStorage/state.vscdb, SQLite, opened read-only): composerData
// rows are sessions and bubbleId rows are messages. Assistant bubbles that
// carry a tokenCount become usage events; user bubbles with text become
// activity and prompts. See docs/agents/SPEC.md "Cursor".
//
// This is a whole-file source: Cursor rewrites the database in place, so the
// parser re-reads every row when the file's size or mtime changed and relies
// on content-derived ids for idempotency. Rows are streamed through SQLite's
// json_extract, so the multi-gigabyte file is never loaded into memory.
package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const (
	pv = 1

	// Provider and Source are both "cursor" (SPEC "Providers"): Cursor is
	// its own provider with its own model catalogue.
	ProviderName = "cursor"
	SourceName   = "cursor"

	// DBEnv overrides the database path (tests).
	DBEnv = "D0M1_CURSOR_DB"

	dbFile   = "state.vscdb"
	busyWait = 5 * time.Second
)

// useSQLJSON selects the fast path (SQLite parses each row's JSON). Tests
// flip it to cover the Go-side decoder, which is also the fallback when a
// row holds malformed JSON (json_extract then fails the whole statement).
var useSQLJSON = true

type Source struct{}

// New returns the Cursor source.
func New() sources.Source { return &Source{} }

func (*Source) Name() string                   { return SourceName }
func (*Source) Provider() string               { return ProviderName }
func (*Source) PV() int                        { return pv }
func (*Source) Prepare(env *sources.Env) error { return nil }

// UserDir is Cursor's per-user data directory (holding globalStorage and
// workspaceStorage) for the given home directory.
func UserDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Cursor", "User")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User")
	default:
		return filepath.Join(home, ".config", "Cursor", "User")
	}
}

// DBPath is the global state database for home, or the DBEnv override.
func DBPath(home string) string {
	if v := os.Getenv(DBEnv); v != "" {
		return v
	}
	return filepath.Join(UserDir(home), "globalStorage", dbFile)
}

// Files returns the database when it exists.
func (*Source) Files(env *sources.Env) ([]string, error) {
	p := DBPath(env.Home)
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	return []string{p}, nil
}

// Parse re-reads every session and message row (whole-file source) and sets
// the cursor's Offset to the file size.
func (*Source) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	next := cur
	next.Carry = nil
	if st, err := os.Stat(path); err == nil {
		next.Offset = st.Size()
	}
	db, err := open(path)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer db.Close()
	e := &jsonl.Emitter{Env: env, Provider: ProviderName, Source: SourceName, PV: pv}
	r := &reader{db: db, e: e, workspaces: workspaceMap(path)}
	if err := r.run(); err != nil {
		return sources.Batch{}, cur, err
	}
	return e.Batch, next, nil
}

// --- database access ---

// open opens the database read-only, waiting out Cursor's own write locks.
// It never takes a write lock: mode=ro plus query_only.
func open(path string) (*sql.DB, error) {
	u := url.URL{Path: filepath.ToSlash(path)}
	dsn := "file:" + u.EscapedPath() + "?mode=ro&_pragma=busy_timeout(" +
		fmt.Sprint(busyWait.Milliseconds()) + ")&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// A WAL database needs its -shm file; with nobody writing, an immutable
	// open reads the main file alone.
	if err := db.Ping(); err != nil {
		db.Close()
		if _, werr := os.Stat(path + "-wal"); werr == nil {
			return nil, err
		}
		db, err = sql.Open("sqlite", dsn+"&immutable=1")
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

const (
	composerPrefix = "composerData:"
	bubblePrefix   = "bubbleId:"
)

// rangeWhere bounds key to one prefix so SQLite walks the key index.
func rangeWhere(prefix string) string {
	end := prefix[:len(prefix)-1] + string(prefix[len(prefix)-1]+1)
	return "key >= '" + prefix + "' AND key < '" + end + "'"
}

// composerSQL and bubbleSQL project only the fields the parser needs, as one
// JSON array per row, so each value is parsed once by SQLite and the row's
// full JSON (up to tens of MB) never crosses into Go.
var (
	composerSQL = "SELECT key, json_extract(value, '$.createdAt', '$.modelConfig.modelName', '$.fullConversationHeadersOnly') " +
		"FROM cursorDiskKV WHERE " + rangeWhere(composerPrefix) + " AND value IS NOT NULL"
	bubbleSQL = "SELECT key, json_extract(value, '$.type', '$.createdAt', '$.tokenCount.inputTokens', '$.tokenCount.outputTokens', " +
		"'$.usageUuid', '$.modelInfo.modelName', '$.workspaceProjectDir', '$.text', '$.images[0]') " +
		"FROM cursorDiskKV WHERE " + rangeWhere(bubblePrefix) + " AND value IS NOT NULL ORDER BY key"
	composerRawSQL = "SELECT key, value FROM cursorDiskKV WHERE " + rangeWhere(composerPrefix) + " AND value IS NOT NULL"
	bubbleRawSQL   = "SELECT key, value FROM cursorDiskKV WHERE " + rangeWhere(bubblePrefix) + " AND value IS NOT NULL ORDER BY key"
)

// --- row shapes ---

type header struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"`
}

// composer is what a session contributes to its bubbles.
type composer struct {
	CreatedAt time.Time
	Model     string
	Headers   []header // conversational order
}

// composerRow is the Go-side decode of a composerData value.
type composerRow struct {
	CreatedAt   json.RawMessage `json:"createdAt"`
	ModelConfig struct {
		ModelName string `json:"modelName"`
	} `json:"modelConfig"`
	Headers []header `json:"fullConversationHeadersOnly"`
}

// bubbleRow is the Go-side decode of a bubbleId value.
type bubbleRow struct {
	Type       int             `json:"type"`
	CreatedAt  json.RawMessage `json:"createdAt"`
	TokenCount *struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"tokenCount"`
	UsageUUID string `json:"usageUuid"`
	ModelInfo struct {
		ModelName string `json:"modelName"`
	} `json:"modelInfo"`
	WorkspaceProjectDir string            `json:"workspaceProjectDir"`
	Text                string            `json:"text"`
	Images              []json.RawMessage `json:"images"`
}

const (
	typeUser      = 1
	typeAssistant = 2
)

// bubble is one message with only the parsed fields.
type bubble struct {
	composerID string
	id         string
	typ        int
	ts         time.Time // zero when the row has none
	in, out    int64
	hasTokens  bool // tokenCount present with at least one non-zero field
	usageUUID  string
	model      string
	workspace  string
	text       string
	image      bool
}

// --- reading ---

type reader struct {
	db         *sql.DB
	e          *jsonl.Emitter
	workspaces map[string]string // composerId -> folder, from workspaceStorage
	composers  map[string]*composer

	// Per-composer state while its bubbles stream in (rows come grouped by
	// composer because they are ordered by key).
	curComposer string
	users       []bubble
	models      map[string]string // assistant bubbleId -> its own model
	anyTokens   bool
}

func (r *reader) run() error {
	r.composers = map[string]*composer{}
	err := r.readComposers(useSQLJSON)
	if err != nil && useSQLJSON && malformed(err) {
		err = r.readComposers(false)
	}
	if err != nil {
		return err
	}
	err = r.readBubbles(useSQLJSON)
	if err != nil && useSQLJSON && malformed(err) {
		r.reset()
		r.e.Batch = sources.Batch{}
		err = r.readBubbles(false)
	}
	if err != nil {
		return err
	}
	r.flush()
	return nil
}

// malformed reports SQLite refusing json_extract on an invalid value.
func malformed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "malformed JSON")
}

func (r *reader) readComposers(sqlJSON bool) error {
	q := composerRawSQL
	if sqlJSON {
		q = composerSQL
	}
	rows, err := r.db.QueryContext(context.Background(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var val []byte
		if err := rows.Scan(&key, &val); err != nil {
			return err
		}
		id := strings.TrimPrefix(key, composerPrefix)
		var c composerRow
		if sqlJSON {
			var parts []json.RawMessage
			if json.Unmarshal(val, &parts) != nil || len(parts) != 3 {
				continue
			}
			c.CreatedAt = parts[0]
			json.Unmarshal(parts[1], &c.ModelConfig.ModelName)
			json.Unmarshal(parts[2], &c.Headers)
		} else if !jsonl.Decode(val, &c) {
			continue
		}
		r.composers[id] = &composer{CreatedAt: parseTS(c.CreatedAt), Model: c.ModelConfig.ModelName, Headers: c.Headers}
	}
	return rows.Err()
}

func (r *reader) readBubbles(sqlJSON bool) error {
	q := bubbleRawSQL
	if sqlJSON {
		q = bubbleSQL
	}
	rows, err := r.db.QueryContext(context.Background(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var val []byte
		if err := rows.Scan(&key, &val); err != nil {
			return err
		}
		// bubbleId:<composerId>:<bubbleId>
		rest := strings.TrimPrefix(key, bubblePrefix)
		cid, bid, ok := strings.Cut(rest, ":")
		if !ok || cid == "" || bid == "" {
			continue
		}
		var b bubble
		var decoded bool
		if sqlJSON {
			b, decoded = decodeSQLBubble(val)
		} else {
			b, decoded = decodeRawBubble(val)
		}
		if !decoded {
			continue
		}
		b.composerID, b.id = cid, bid
		r.handle(b)
	}
	return rows.Err()
}

// decodeSQLBubble reads the json_extract projection (see bubbleSQL).
func decodeSQLBubble(val []byte) (bubble, bool) {
	var p []json.RawMessage
	if json.Unmarshal(val, &p) != nil || len(p) != 9 {
		return bubble{}, false
	}
	var b bubble
	var typ float64
	if json.Unmarshal(p[0], &typ) != nil {
		return bubble{}, false
	}
	b.typ = int(typ)
	b.ts = parseTS(p[1])
	var in, out *int64
	json.Unmarshal(p[2], &in)
	json.Unmarshal(p[3], &out)
	if in != nil || out != nil {
		b.in, b.out = deref(in), deref(out)
		b.hasTokens = b.in > 0 || b.out > 0
	}
	json.Unmarshal(p[4], &b.usageUUID)
	json.Unmarshal(p[5], &b.model)
	json.Unmarshal(p[6], &b.workspace)
	json.Unmarshal(p[7], &b.text)
	b.image = !isNull(p[8])
	return b, true
}

func decodeRawBubble(val []byte) (bubble, bool) {
	var row bubbleRow
	if !jsonl.Decode(val, &row) || row.Type == 0 {
		return bubble{}, false
	}
	b := bubble{
		typ:       row.Type,
		ts:        parseTS(row.CreatedAt),
		usageUUID: row.UsageUUID,
		model:     row.ModelInfo.ModelName,
		workspace: row.WorkspaceProjectDir,
		text:      row.Text,
		image:     len(row.Images) > 0,
	}
	if row.TokenCount != nil {
		b.in, b.out = row.TokenCount.InputTokens, row.TokenCount.OutputTokens
		b.hasTokens = b.in > 0 || b.out > 0
	}
	return b, true
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func isNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

// parseTS reads a createdAt that is either epoch milliseconds (a JSON number,
// as composers write it) or an RFC 3339 string (as bubbles write it).
func parseTS(raw json.RawMessage) time.Time {
	if isNull(raw) {
		return time.Time{}
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return time.Time{}
		}
		if n < 1e11 { // seconds
			n *= 1000
		}
		return time.UnixMilli(int64(n)).UTC()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, ok := jsonl.ParseTime(s); ok {
			return t
		}
	}
	return time.Time{}
}

// handle takes one bubble in key order (grouped by composer).
func (r *reader) handle(b bubble) {
	if b.composerID != r.curComposer {
		r.flush()
		r.curComposer = b.composerID
	}
	c := r.composers[b.composerID]
	switch b.typ {
	case typeAssistant:
		if b.model != "" {
			r.models[b.id] = b.model
		}
		if !b.hasTokens {
			return
		}
		r.anyTokens = true
		ts := b.ts
		if ts.IsZero() && c != nil {
			ts = c.CreatedAt
		}
		if ts.IsZero() {
			return
		}
		m := b.model
		if m == "" && c != nil {
			m = c.Model
		}
		key := b.usageUUID
		if key == "" {
			key = b.composerID + ":" + b.id
		}
		r.e.Dir = b.workspace
		if r.e.Dir == "" {
			r.e.Dir = r.workspaces[b.composerID]
		}
		r.e.Usage(key, ts, b.composerID, m, model.Tokens{In: b.in, Out: b.out, Calls: 1})
	case typeUser:
		if strings.TrimSpace(b.text) == "" {
			if !b.image {
				return
			}
			b.text = "[image]"
		}
		r.users = append(r.users, b)
	}
}

func (r *reader) reset() {
	r.curComposer = ""
	r.users = nil
	r.models = map[string]string{}
	r.anyTokens = false
}

// flush emits the finished composer's prompts: the model is the next
// assistant bubble's own model in conversational order, else the composer's.
func (r *reader) flush() {
	c := r.composers[r.curComposer]
	for _, u := range r.users {
		ts := u.ts
		if ts.IsZero() && c != nil {
			ts = c.CreatedAt
		}
		if ts.IsZero() {
			continue
		}
		key := u.composerID + ":" + u.id
		ws := u.workspace
		if ws == "" {
			ws = r.workspaces[u.composerID]
		}
		r.e.Dir = ws
		r.e.Activity(key, ts, u.composerID, r.anyTokens)
		if !r.e.Prompts() {
			continue
		}
		p := r.e.Prompt(key, ts, u.composerID, ws, u.text)
		p.Model = r.nextModel(c, u.id)
		r.e.Emit(p)
	}
	r.reset()
}

func (r *reader) nextModel(c *composer, bubbleID string) string {
	if c == nil {
		return ""
	}
	at := -1
	for i, h := range c.Headers {
		if h.BubbleID == bubbleID {
			at = i
			break
		}
	}
	if at >= 0 {
		for _, h := range c.Headers[at+1:] {
			if h.Type != typeAssistant {
				continue
			}
			if m := r.models[h.BubbleID]; m != "" {
				return m
			}
			break
		}
	}
	return c.Model
}

// --- workspace folders ---

// workspaceMap reads User/workspaceStorage/<hash>/{workspace.json,state.vscdb}
// next to the global database: each workspace lists its composer ids under
// ItemTable key composer.composerData, which is the only place Cursor records
// a session's folder. Any failure just leaves composers without a folder.
func workspaceMap(dbPath string) map[string]string {
	out := map[string]string{}
	root := filepath.Join(filepath.Dir(filepath.Dir(dbPath)), "workspaceStorage")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		folder := workspaceFolder(filepath.Join(dir, "workspace.json"))
		if folder == "" {
			continue
		}
		for _, id := range workspaceComposers(filepath.Join(dir, dbFile)) {
			out[id] = folder
		}
	}
	return out
}

func workspaceFolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var w struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(b, &w) != nil {
		return ""
	}
	uri := w.Folder
	if uri == "" {
		uri = w.Workspace
	}
	return uriPath(uri)
}

// uriPath turns a file: URI into a local path; other schemes (remote
// workspaces) are kept as written.
func uriPath(uri string) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' { // /c:/Users -> c:/Users
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func workspaceComposers(dbPath string) []string {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	db, err := open(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	var val []byte
	err = db.QueryRowContext(context.Background(), "SELECT value FROM ItemTable WHERE key = 'composer.composerData'").Scan(&val)
	if err != nil {
		return nil
	}
	var v struct {
		AllComposers []struct {
			ComposerID string `json:"composerId"`
		} `json:"allComposers"`
	}
	if !jsonl.Decode(val, &v) {
		return nil
	}
	ids := make([]string, 0, len(v.AllComposers))
	for _, c := range v.AllComposers {
		if c.ComposerID != "" {
			ids = append(ids, c.ComposerID)
		}
	}
	return ids
}

// --- account ---

// Account reads the signed-in Cursor account from ItemTable: the stable id
// is the access token's JWT subject (cursorAuth/accessToken), falling back
// to the cached email, which is also the label. Nothing is returned when
// Cursor is not installed or nobody is signed in.
func Account(home string) (nativeID, label string, ok bool) {
	path := DBPath(home)
	if _, err := os.Stat(path); err != nil {
		return "", "", false
	}
	db, err := open(path)
	if err != nil {
		return "", "", false
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(),
		"SELECT key, value FROM ItemTable WHERE key IN ('cursorAuth/accessToken', 'cursorAuth/cachedEmail')")
	if err != nil {
		return "", "", false
	}
	defer rows.Close()
	var token, email string
	for rows.Next() {
		var k string
		var v []byte
		if rows.Scan(&k, &v) != nil {
			continue
		}
		switch k {
		case "cursorAuth/accessToken":
			token = string(v)
		case "cursorAuth/cachedEmail":
			email = strings.TrimSpace(string(v))
		}
	}
	if sub := jwtSubject(token); sub != "" {
		return sub, email, true
	}
	if email != "" {
		return email, email, true
	}
	return "", "", false
}

// jwtSubject returns the sub claim of an unverified JWT ("" if none).
func jwtSubject(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return ""
	}
	return claims.Sub
}
