// Package webclaude is Claude Web capture's local storage, shared by the
// native-messaging host (internal/nativehost), which writes it, and the
// web-claude source (internal/sources/claudeweb), which reads it. See
// docs/agents/SPEC.md "Claude chat".
//
// The browser extension (collector/extension) sends one Record per Claude
// chat turn: ids, times, the model and an estimate in numbers. It never
// sends cookies or headers, and sends the prompt text only when the
// collector's prompt capture is on. Org and conversation ids stay in the
// owner-only spool; the collector hashes them before anything leaves.
package webclaude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

// Record kinds.
const (
	// KindLive is a turn the extension saw being submitted in a browser
	// tab: its client is known.
	KindLive = "live"
	// KindSweep is a turn read from the account's conversation history by
	// the opt-in sweeper. Its client is not recorded (SPEC "Claude chat").
	KindSweep = "sweep"
	// KindClaim is sent the moment a turn's stream ends in a browser tab,
	// before its live record (which needs the conversation read back a few
	// seconds later): ids and the client platform, no estimate and no text.
	// The host keeps it as a pending turn (Pending), never in the spool.
	KindClaim = "claim"
)

// Client platforms, from the anthropic-client-platform request header.
const (
	PlatformWeb     = "web_claude_ai"
	PlatformDesktop = "desktop_app"
)

// Algorithm is the estimator id the extension writes (estimate.js).
const Algorithm = "chars-v1"

// Event versions: a live record outranks a sweep inference of the same
// turn on the server (merge.js keeps the higher pv wholesale).
const (
	PVSweep = 1
	PVLive  = 2
)

// Estimate is the extension's token estimate for one turn: a lower bound
// (no memory, project knowledge or hidden thinking). In and Out are the
// conversation; Ctx is the fixed context each model call re-reads: the
// request's tool definitions (live turns only) plus a floor for the system
// prompt, which the page never sees. The web-claude source reports
// Ctx x calls as estimated cache reads.
type Estimate struct {
	In   int64  `json:"in"`
	Out  int64  `json:"out"`
	Ctx  int64  `json:"ctx,omitempty"`
	Algo string `json:"algo"`
}

// Record is one line of the spool.
type Record struct {
	V            int      `json:"v"`
	Kind         string   `json:"kind"`
	Platform     string   `json:"platform,omitempty"`
	Org          string   `json:"org"`
	Conv         string   `json:"conv"`
	Human        string   `json:"human"`
	Assistant    string   `json:"assistant"`
	TS           string   `json:"ts"`
	PromptTS     string   `json:"promptTs,omitempty"`
	Model        string   `json:"model,omitempty"`
	ChatPlatform string   `json:"chatPlatform,omitempty"`
	Stop         string   `json:"stop,omitempty"`
	Calls        int64    `json:"calls"`
	ThinkingMs   int64    `json:"thinkingMs,omitempty"`
	Est          Estimate `json:"est"`
	// Text is the prompt, present only while prompt capture is on.
	Text *string `json:"text,omitempty"`
	// Ext is the extension version.
	Ext string `json:"ext,omitempty"`
	// At is when the host accepted the record. The host sets it; a record
	// that arrives with it is rejected.
	At string `json:"at,omitempty"`
}

// Bounds. A turn's estimated input sums its whole context once per model
// call, so it can be large; anything past these is a bug or a forgery.
const (
	MaxCalls = 1000
	MaxIn    = 2_000_000_000
	MaxOut   = 20_000_000
	MaxCtx   = 2_000_000
	// MaxInPerCall and MaxOutPerCall bound a turn by its model calls: no
	// call reads more than a context window or writes more than its output
	// limit, so a forged record cannot inflate the totals without bound.
	MaxInPerCall  = 2_000_000
	MaxOutPerCall = 200_000
	MaxThinkingMs = 24 * 60 * 60 * 1000
	// MaxRecordBytes caps one spool line (a prompt is at most
	// model.MaxPromptBytes; the rest is small). The host encodes with Encode
	// and cuts the text until the line fits, so every line it writes decodes.
	MaxRecordBytes = model.MaxPromptBytes + 8<<10
	// MaxRawRecordBytes caps one record as the extension sends it: JSON
	// escaping can make one byte of text six (a control character), so the
	// host allows for that before checking the decoded text's length.
	MaxRawRecordBytes = 6*model.MaxPromptBytes + 8<<10
)

var (
	uuidRE      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	modelRE     = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]{0,80}$`)
	upperRE     = regexp.MustCompile(`^[A-Z_]{0,32}$`)
	lowerRE     = regexp.MustCompile(`^[a-z_]{0,32}$`)
	algoRE      = regexp.MustCompile(`^[a-z0-9.-]{1,16}$`)
	extRE       = regexp.MustCompile(`^[0-9.]{0,16}$`)
	minRecordTS = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Decode parses one spool line strictly: unknown fields, trailing data and
// input over MaxRecordBytes are errors. Errors never quote the input.
func Decode(b []byte) (Record, error) { return decode(b, MaxRecordBytes) }

// DecodeHost is Decode for a record from the extension, whose JSON may be up
// to MaxRawRecordBytes (escaped text); Validate still bounds the text.
func DecodeHost(b []byte) (Record, error) { return decode(b, MaxRawRecordBytes) }

func decode(b []byte, max int) (Record, error) {
	var r Record
	if len(b) > max {
		return r, errors.New("record too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, errors.New("record is not valid JSON of the record schema")
	}
	if d.More() {
		return r, errors.New("trailing data after record")
	}
	return r, nil
}

// Encode writes r as one spool line (no trailing newline). Unlike
// json.Marshal it leaves < > & unescaped, as the extension's JSON.stringify
// does, so a line is never three times longer than the text it carries.
func Encode(r *Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Validate checks a record. host is true for a record arriving from the
// extension (At must be absent); the source re-validates spool lines with
// host false. Error messages name the field, never its value.
func (r *Record) Validate(now time.Time, host bool) error {
	bad := func(field string) error { return fmt.Errorf("invalid %s", field) }
	switch {
	case r.V != 1:
		return bad("v")
	case r.Kind != KindLive && r.Kind != KindSweep && !(host && r.Kind == KindClaim):
		return bad("kind")
	case r.Kind == KindClaim && (r.Est.In != 0 || r.Est.Out != 0 || r.Est.Ctx != 0 || r.Text != nil):
		return bad("claim")
	case r.Platform != "" && r.Platform != PlatformWeb && r.Platform != PlatformDesktop:
		return bad("platform")
	case !uuidRE.MatchString(r.Org):
		return bad("org")
	case !uuidRE.MatchString(r.Conv):
		return bad("conv")
	case !uuidRE.MatchString(r.Human) && !(r.Kind == KindClaim && r.Human == ""):
		return bad("human")
	case !uuidRE.MatchString(r.Assistant) || r.Assistant == r.Human:
		return bad("assistant")
	case !modelRE.MatchString(r.Model):
		return bad("model")
	case !upperRE.MatchString(r.ChatPlatform):
		return bad("chatPlatform")
	case !lowerRE.MatchString(r.Stop):
		return bad("stop")
	case r.Calls < 1 || r.Calls > MaxCalls:
		return bad("calls")
	case r.ThinkingMs < 0 || r.ThinkingMs > MaxThinkingMs:
		return bad("thinkingMs")
	case r.Est.In < 0 || r.Est.In > MaxIn || r.Est.In > r.Calls*MaxInPerCall:
		return bad("est.in")
	case r.Est.Out < 0 || r.Est.Out > MaxOut || r.Est.Out > r.Calls*MaxOutPerCall:
		return bad("est.out")
	case r.Est.Ctx < 0 || r.Est.Ctx > MaxCtx:
		return bad("est.ctx")
	case !algoRE.MatchString(r.Est.Algo):
		return bad("est.algo")
	case !extRE.MatchString(r.Ext):
		return bad("ext")
	case host && r.At != "":
		return bad("at")
	case r.Text != nil && len(*r.Text) > model.MaxPromptBytes+1024:
		return bad("text")
	}
	ts, ok := parseTime(r.TS)
	if !ok || ts.Before(minRecordTS) || ts.After(now.Add(48*time.Hour)) {
		return bad("ts")
	}
	if r.PromptTS != "" {
		pt, ok := parseTime(r.PromptTS)
		if !ok || pt.Before(minRecordTS) || pt.After(ts.Add(time.Minute)) {
			return bad("promptTs")
		}
	}
	if !host && r.At != "" {
		if _, ok := parseTime(r.At); !ok {
			return bad("at")
		}
	}
	return nil
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Time is the assistant message time; PromptTime the human message time
// (the assistant's when absent); AtTime when the host accepted it (zero
// when absent). Call them on a validated record.
func (r *Record) Time() time.Time { t, _ := parseTime(r.TS); return t }

func (r *Record) PromptTime() time.Time {
	if t, ok := parseTime(r.PromptTS); ok {
		return t
	}
	return r.Time()
}

func (r *Record) AtTime() time.Time { t, _ := parseTime(r.At); return t }

// UsageKey and PromptKey are the native keys of a turn's usage and of its
// prompt (activity and prompt record): message uuids, the same from every
// client and every read.
func UsageKey(assistant string) string { return "msg:" + normUUID(assistant) }
func PromptKey(human string) string    { return "prompt:" + normUUID(human) }

func normUUID(s string) string { return string(bytes.ToLower([]byte(s))) }

// UsageID is the canonical usage event id of an assistant message.
func UsageID(assistant string) string {
	return model.EventID(model.KindUsage, model.ProviderAnthropic, model.IDSourceClaudeChat, UsageKey(assistant))
}
