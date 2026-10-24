// Package claudeweb reads the Claude Web capture spool (internal/webclaude)
// that tokenmaxr's native-messaging host writes for the browser extension.
// Every token figure it emits is estimated (SPEC "Claude chat"): claude.ai
// exposes no token counters for chat.
//
// One upstream turn has one id in every channel (model.IDSourceClaudeChat),
// so a turn counts once whichever channel reports it:
//   - a live record is the client that submitted it: web-claude, or
//     native-claude for the desktop app (pv 2);
//   - a swept record (history, client not recorded, pv 1) is skipped when
//     this machine recorded the turn live, goes to the client's channel when
//     this machine's browser saw the turn end but its live record never came
//     (a pending claim: the tab closed first), goes to chat-claude when it is
//     older than the history cut (this machine's capture start T0, or the
//     fleet's earlier one, Settings.ChatBefore), is skipped from that cut
//     to T0 (another machine's browser may have recorded it), and otherwise
//     goes to native-claude (inferred: an app turn, no browser saw it)
//     unless the owner turned that inference off.
//
// Prompt text is kept only when both the collector's prompt capture and the
// Claude chat prompt setting (Settings.TextAllowed) allow it for the
// record's kind. Once the collector's cursor has passed a spool line with
// text (the prompt record is in the outbox), the next read deletes that text
// from the spool (webclaude.ScrubText); the cursor's carry marks a file
// with text still to delete, so the scan offers it again.
package claudeweb

import (
	"encoding/json"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// pv is the parser version (cursor resets); event versions are
// webclaude.PVLive and PVSweep.
const pv = 1

type Source struct {
	t0       time.Time
	settings webclaude.Settings
	claims   map[string]bool
	pending  map[string]string
}

func New() sources.Source { return &Source{} }

func (*Source) Name() string     { return model.SourceClaudeWeb }
func (*Source) Provider() string { return model.ProviderAnthropic }
func (*Source) PV() int          { return pv }

// RewritesFiles: Parse scrubs prompt text from lines the cursor passed
// (sources.Rewriter).
func (*Source) RewritesFiles() bool { return true }

func (s *Source) Prepare(env *sources.Env) error {
	if env == nil || env.WebClaude == "" {
		return nil
	}
	s.t0 = webclaude.LoadEnrolled(env.WebClaude).T0
	s.settings = webclaude.LoadSettings(env.WebClaude)
	s.claims = webclaude.Claims(env.WebClaude)
	s.pending = webclaude.PendingClaims(env.WebClaude)
	return nil
}

func (*Source) Files(env *sources.Env) ([]string, error) {
	if env == nil || env.WebClaude == "" {
		return nil, nil
	}
	return webclaude.SpoolFiles(env.WebClaude)
}

// Classification of one record.
type Class struct {
	Source string
	PV     int
	// Skip: the turn is reported elsewhere (claimed live) or must not be
	// guessed (inference off, or swept before it settled).
	Skip bool
}

// clientSource is the channel of a turn whose client platform is known.
func clientSource(platform string) string {
	if platform == webclaude.PlatformDesktop {
		return model.SourceClaudeNative
	}
	return model.SourceClaudeWeb
}

// Classify applies the channel rules in the package comment. claimed reports
// a turn recorded live on this machine; pending returns the client platform
// of a turn this machine's browser saw end (nil: none).
func Classify(r *webclaude.Record, claimed func(id string) bool, pending func(id string) (string, bool), t0 time.Time, set webclaude.Settings) Class {
	if r.Kind == webclaude.KindLive {
		return Class{Source: clientSource(r.Platform), PV: webclaude.PVLive}
	}
	id := webclaude.UsageID(r.Assistant)
	if claimed != nil && claimed(id) {
		return Class{Skip: true}
	}
	// Typed in this browser, but the live record never arrived: the sweep's
	// estimate, in the client's channel (a live record still outranks it).
	if pending != nil {
		if plat, ok := pending(id); ok {
			return Class{Source: clientSource(plat), PV: webclaude.PVSweep}
		}
	}
	ts := r.Time()
	cut := set.HistoryCut(t0)
	if cut.IsZero() || ts.Before(cut) {
		return Class{Source: model.SourceClaudeChat, PV: webclaude.PVSweep}
	}
	// From the fleet's capture start to this machine's, a turn may have
	// been recorded live by another machine, whose claims are not visible
	// here: it is never reported from the sweep.
	if t0.IsZero() || ts.Before(t0) {
		return Class{Skip: true}
	}
	// A turn swept before the settle window ended might still be claimed
	// by a live record on another machine: do not guess yet.
	if at := r.AtTime(); !at.IsZero() && at.Sub(ts) < set.Settle() {
		return Class{Skip: true}
	}
	if !set.Infer() {
		return Class{Skip: true}
	}
	return Class{Source: model.SourceClaudeNative, PV: webclaude.PVSweep}
}

// carry is the cursor carry: Scrub counts the reads since the file got
// prompt text that is not yet deleted (0: none).
type carry struct {
	Scrub int `json:"scrub,omitempty"`
}

// maxScrubTries stops re-offering a file whose text cannot be deleted.
const maxScrubTries = 100

func (s *Source) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	e := &jsonl.Emitter{Env: env, Provider: model.ProviderAnthropic, Q: model.QualityEstimated, IDSource: model.IDSourceClaudeChat}
	var c carry
	if len(cur.Carry) > 0 {
		_ = json.Unmarshal(cur.Carry, &c)
	}
	if c.Scrub > 0 && cur.Offset > 0 && scrubs(env) {
		if !scrub(env.WebClaude, path, cur.Offset) {
			c.Scrub++
		} else {
			c.Scrub = 0
		}
	}
	r, err := jsonl.Open(path, cur.Offset)
	if err != nil {
		return sources.Batch{}, cur, err
	}
	defer r.Close()
	now := time.Now()
	claimed := func(id string) bool { return s.claims[id] }
	pending := func(id string) (string, bool) { p, ok := s.pending[id]; return p, ok }
	for {
		b, ok, err := r.Next()
		if err != nil {
			return sources.Batch{}, cur, err
		}
		if !ok {
			break
		}
		rec, err := webclaude.Decode(b)
		if err != nil || rec.Validate(now, false) != nil {
			continue
		}
		if rec.Text != nil && c.Scrub == 0 {
			c.Scrub = 1
		}
		cl := Classify(&rec, claimed, pending, s.t0, s.settings)
		if cl.Skip {
			continue
		}
		if !s.settings.TextAllowed(rec.Kind) {
			rec.Text = nil
		}
		Emit(e, &rec, cl)
	}
	next := cur
	next.Offset = r.Offset()
	next.Carry = nil
	if c.Scrub > 0 && c.Scrub <= maxScrubTries {
		next.Carry, _ = json.Marshal(c)
	}
	return e.Batch, next, nil
}

// scrubs reports whether this read may change the spool: a collector scan,
// which remembers state, not a dry run or a probe (Env.ScrubSpool).
func scrubs(env *sources.Env) bool { return env != nil && env.WebClaude != "" && env.ScrubSpool }

// scrub deletes the prompt text of the spool lines before offset: the
// cursor passed them in a scan that completed, so their prompt records are
// in the outbox. It reports whether no text is left there.
func scrub(dir, path string, offset int64) bool {
	l, err := webclaude.Lock(dir)
	if err != nil {
		return false
	}
	defer l.Release()
	_, left, err := webclaude.ScrubText(path, offset)
	return err == nil && !left
}

// Emit appends one turn's events: estimated usage, the prompt's activity
// (with usage, so it is not counted as a prompt without token records) and,
// when the record carries text and capture is on, the prompt record.
func Emit(e *jsonl.Emitter, r *webclaude.Record, c Class) {
	e.Source, e.PV = c.Source, c.PV
	ts, pts := r.Time(), r.PromptTime()
	e.Hint = jsonl.HintFor(e.Env, model.ProviderAnthropic, sources.HintOrg, r.Org, ts)
	// The fixed context (tool definitions and the system prompt floor) is
	// re-read by every model call of the turn: estimated cache reads.
	e.Usage(webclaude.UsageKey(r.Assistant), ts, r.Conv, r.Model, model.Tokens{In: r.Est.In, Out: r.Est.Out, CacheR: r.Est.Ctx * r.Calls, Calls: r.Calls})
	e.Activity(webclaude.PromptKey(r.Human), pts, r.Conv, true)
	if r.Text != nil && *r.Text != "" && e.Prompts() {
		p := e.Prompt(webclaude.PromptKey(r.Human), pts, r.Conv, "", *r.Text)
		p.Model = r.Model
		e.Emit(p)
	}
}
