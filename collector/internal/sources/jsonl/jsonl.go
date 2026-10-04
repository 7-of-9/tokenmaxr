// Package jsonl reads newline-delimited JSON logs that other processes are
// still appending to, plus the small helpers the source parsers share.
package jsonl

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/workspace"
)

// MaxLine bounds the memory one line may take. Longer lines (huge tool output
// or base64 images) are consumed but returned empty, so parsing moves past them.
const MaxLine = 256 << 20

// Reader returns complete '\n'-terminated lines from an offset. A trailing
// line without '\n' is never returned or counted, because its writer may
// still be appending to it.
type Reader struct {
	f   *os.File
	br  *bufio.Reader
	off int64
	buf []byte
}

// Open opens path with fsx.Open and positions it at offset.
func Open(path string, offset int64) (*Reader, error) {
	f, err := fsx.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return &Reader{f: f, br: bufio.NewReaderSize(f, 1<<20), off: offset}, nil
}

func (r *Reader) Close() error { return r.f.Close() }

// Offset is the byte offset just past the last complete line returned.
func (r *Reader) Offset() int64 { return r.off }

// Next returns the next complete line without its line ending. ok is false
// at EOF, including when only a partial line remains. The returned slice is
// only valid until the next call.
func (r *Reader) Next() (line []byte, ok bool, err error) {
	chunk, err := r.br.ReadSlice('\n')
	if err == nil {
		r.off += int64(len(chunk))
		return trimEOL(chunk), true, nil
	}
	n := len(chunk)
	over := false
	r.buf = append(r.buf[:0], chunk...)
	for errors.Is(err, bufio.ErrBufferFull) {
		chunk, err = r.br.ReadSlice('\n')
		n += len(chunk)
		if !over && len(r.buf)+len(chunk) > MaxLine {
			over = true
			r.buf = r.buf[:0]
		}
		if !over {
			r.buf = append(r.buf, chunk...)
		}
	}
	if errors.Is(err, io.EOF) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	r.off += int64(n)
	if over {
		return nil, true, nil
	}
	return trimEOL(r.buf), true, nil
}

func trimEOL(b []byte) []byte {
	b = b[:len(b)-1]
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

// Decode unmarshals b into v. A field whose JSON type differs from the
// struct is left zero instead of dropping the whole line, so a tool that
// changes one field's shape does not hide the rest of its data.
func Decode(b []byte, v any) bool {
	err := json.Unmarshal(b, v)
	var te *json.UnmarshalTypeError
	return err == nil || errors.As(err, &te)
}

// ReadFile reads a whole (small) file through fsx.Open.
func ReadFile(path string) ([]byte, error) {
	f, err := fsx.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Truncate caps s at max bytes, ending it with a marker that gives the
// original size. It never splits a UTF-8 sequence.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	marker := fmt.Sprintf("\n[truncated by the collector: original %d bytes]", len(s))
	cut := max - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// PromptText trims a prompt and applies the MaxPromptBytes cap.
func PromptText(s string) string {
	return Truncate(strings.TrimSpace(s), model.MaxPromptBytes)
}

// ParseTime parses an RFC 3339 timestamp as written by the tools.
func ParseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Attribute returns the account for an event, or "unknown" when the core
// supplied no attribution.
func Attribute(env *sources.Env, provider string, ts time.Time, sessionID string, h sources.Hint) (string, string) {
	if env == nil || env.Attribute == nil {
		return "", model.AcctUnknown
	}
	return env.Attribute(provider, ts, sessionID, h)
}

// HintFor hashes a native identity id at once and returns it as a stream
// hint; kind is sources.HintAccount or sources.HintOrg (hashed as
// "org:<uuid>"). Without a key in env it returns the zero hint.
func HintFor(env *sources.Env, provider, kind, nativeID string, at time.Time) sources.Hint {
	nativeID = strings.TrimSpace(nativeID)
	if env == nil || env.HashID == nil || nativeID == "" {
		return sources.Hint{}
	}
	if kind == sources.HintOrg {
		nativeID = "org:" + nativeID
	}
	return sources.Hint{Kind: kind, ID: env.HashID(provider, nativeID), At: at.UTC()}
}

// TZ returns the machine's UTC offset in minutes at ts.
func TZ(env *sources.Env, ts time.Time) int {
	if env != nil && env.TZOffsetMin != nil {
		return env.TZOffsetMin(ts)
	}
	_, off := ts.In(time.Local).Zone()
	return off / 60
}

// Label returns the private account label for acct, or "".
func Label(env *sources.Env, acct string) string {
	if env == nil || env.Label == nil || acct == "" {
		return ""
	}
	return env.Label(acct)
}

// Emitter builds events for one provider/source pair and tracks the newest
// event time, so each parser only supplies what is specific to it. Hint is
// the stream's current identity hint, passed to every attribution; a parser
// sets it as it reads identity records.
type Emitter struct {
	Env      *sources.Env
	Provider string
	Source   string
	PV       int
	Hint     sources.Hint
	Batch    sources.Batch
	// Dir is the raw folder of the events emitted next: the same cwd the
	// parser passes to Prompt. Usage and activity store its git root.
	Dir string
}

// place is the workspace Prompt would store for Dir.
func (e *Emitter) place() string {
	if e == nil || strings.TrimSpace(e.Dir) == "" {
		return ""
	}
	home := ""
	if e.Env != nil {
		home = e.Env.Home
	}
	return workspace.RepoRoot(home, e.Dir)
}

// wsID is the uploaded tag for place(): the fleet-keyed hash of the folder,
// so the server can group a workspace's tokens and prompts without learning
// its path. "" without a folder or a key.
func (e *Emitter) wsID() string {
	ws := e.place()
	if ws == "" || e.Env == nil || e.Env.HashID == nil {
		return ""
	}
	h := e.Env.HashID("workspace", ws)
	if len(h) < 18 {
		return ""
	}
	return "w_" + h[2:18]
}

func (e *Emitter) seen(ts time.Time) {
	if ts.After(e.Batch.LastEventTS) {
		e.Batch.LastEventTS = ts
	}
}

// Usage appends an exact usage event.
func (e *Emitter) Usage(nativeKey string, ts time.Time, sessionID, modelName string, t model.Tokens) {
	ts = ts.UTC()
	acct, q := Attribute(e.Env, e.Provider, ts, sessionID, e.Hint)
	e.Batch.Usage = append(e.Batch.Usage, model.UsageEvent{
		ID:          model.EventID(model.KindUsage, e.Provider, e.Source, nativeKey),
		Provider:    e.Provider,
		Source:      e.Source,
		TS:          ts,
		TZOffsetMin: TZ(e.Env, ts),
		Model:       modelName,
		Acct:        acct,
		AcctQ:       q,
		Q:           model.QualityExact,
		PV:          e.PV,
		Session:     model.SessionKey(e.Provider, sessionID),
		Workspace:   e.place(),
		WS:          e.wsID(),
		Tokens:      t,
	})
	e.seen(ts)
}

// Activity appends the activity event for one user prompt.
func (e *Emitter) Activity(nativeKey string, ts time.Time, sessionID string, hasUsage bool) {
	ts = ts.UTC()
	acct, q := Attribute(e.Env, e.Provider, ts, sessionID, e.Hint)
	e.Batch.Activity = append(e.Batch.Activity, model.ActivityEvent{
		ID:          model.EventID(model.KindActivity, e.Provider, e.Source, nativeKey),
		Provider:    e.Provider,
		Source:      e.Source,
		TS:          ts,
		TZOffsetMin: TZ(e.Env, ts),
		Acct:        acct,
		AcctQ:       q,
		Session:     model.SessionKey(e.Provider, sessionID),
		HasUsage:    hasUsage,
		Workspace:   e.place(),
		WS:          e.wsID(),
	})
	e.seen(ts)
}

// PromptLabel is the account label a prompt record carries: the account's
// label only when the attribution is strong enough (model.LabelledQ:
// recorded, session, timeline or bounded). A lineage, inferred or unknown
// attribution carries no label, and the archive groups it as unattributed
// (SPEC "Accounts").
func PromptLabel(env *sources.Env, acct, acctQ string) string {
	if !model.LabelledQ(acctQ) {
		return ""
	}
	return Label(env, acct)
}

// Prompt builds (but does not append) a prompt record with an empty model;
// the parser fills the model and appends it with Emit. The workspace becomes
// its git repository root when that exists locally (workspace.RepoRoot).
func (e *Emitter) Prompt(nativeKey string, ts time.Time, sessionID, ws, text string) model.PromptRecord {
	ts = ts.UTC()
	acct, q := Attribute(e.Env, e.Provider, ts, sessionID, e.Hint)
	machine, home := "", ""
	if e.Env != nil {
		machine, home = e.Env.Machine, e.Env.Home
	}
	return model.PromptRecord{
		ID:          model.EventID(model.KindPrompt, e.Provider, e.Source, nativeKey),
		Provider:    e.Provider,
		Source:      e.Source,
		TS:          ts,
		TZOffsetMin: TZ(e.Env, ts),
		Acct:        acct,
		AcctQ:       q,
		AcctLabel:   PromptLabel(e.Env, acct, q),
		Workspace:   workspace.RepoRoot(home, ws),
		Machine:     machine,
		Session:     model.SessionKey(e.Provider, sessionID),
		Text:        PromptText(text),
	}
}

// Emit appends a finished prompt record.
func (e *Emitter) Emit(p model.PromptRecord) {
	e.Batch.Prompts = append(e.Batch.Prompts, p)
	e.seen(p.TS)
}

// Prompts reports whether prompt capture is on for this scan.
func (e *Emitter) Prompts() bool { return e.Env == nil || e.Env.Prompts }
