package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// Per-request caps from the spec.
const (
	MaxUsage        = 200
	MaxActivity     = 500
	MaxPrompts      = 50
	MaxPromptBytes  = 512 * 1024
	MaxLimits       = 50
	MaxAccountUsage = 90
	// MaxLimit is the starting limit on items per request (all kinds
	// together); MinLimit is the floor for halving after repeated
	// 5xx/timeouts (413 may go lower).
	MaxLimit = MaxUsage + MaxActivity + MaxPrompts
	MinLimit = 10
	// bodyBudget leaves headroom under MaxBody for the envelope and heartbeat.
	bodyBudget   = MaxBody - 64*1024
	promptBudget = MaxPromptBytes - 12*1024
	// MaxAttempts is how many sends the server may answer with "retry" for
	// one id before it is moved to the dead-letter directory, so an id the
	// server never manages to take (e.g. a prompt it cannot encrypt) cannot
	// hold the queue.
	MaxAttempts = 20
)

// Result summarises one upload pass.
type Result struct {
	Requests int
	Accepted int
	// Retried counts ids the server asked for again and that stay queued.
	Retried int
	// Dropped counts lone items the server refused with 413.
	Dropped int
	// Rejected counts ids the server declared invalid (never re-sent).
	Rejected int
	// DeadLettered counts ids moved to outbox/dead (rejected, or retried
	// MaxAttempts times).
	DeadLettered  int
	HeartbeatSent bool
	Unauthorized  bool
	// ServerSkewMs is serverTime minus local time, from the last response.
	ServerSkewMs *int64
	Err          error
}

// Progress describes actual request activity and the durable queue remaining.
type Progress struct {
	InFlight  bool
	Remaining int
	Accepted  int
}

type Uploader struct {
	Progress func(Progress)
	progress Progress
	Client   *Client
	Outbox   *outbox.Outbox
	Log      *logx.Logger
	Version  string
	Now      func() time.Time
	// Sleep is used between immediate re-sends of server "retry" ids.
	Sleep func(time.Duration)
	// Homes are the scan roots of this tick; they ride on the heartbeat.
	Homes []string
}

// Backoff delay after n consecutive failures: 1, 2, 4 ... capped at 30 min.
func backoffDelay(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < 30*time.Minute; i++ {
		d *= 2
	}
	return min(d, 30*time.Minute)
}

// Run drains the outbox oldest-first until it is empty, the deadline passes,
// or the server fails. A file whose leftovers the server keeps answering with
// "retry" is skipped for this pass, never letting it block later files. hb,
// when non-nil, rides on the first request (or goes alone if there is
// nothing queued). bo is updated in place.
func (u *Uploader) Run(ctx context.Context, deadline time.Time, bo *store.Backoff, hb *model.Heartbeat) Result {
	var res Result
	if u.Progress != nil {
		_, u.progress.Remaining = u.Outbox.Count()
		u.progress.InFlight, u.progress.Accepted = false, 0
		u.report()
	}
	if bo.Limit <= 0 || bo.Limit > MaxLimit {
		bo.Limit = MaxLimit
	}
	// Grow a reduced limit back once per pass (not per request, which would
	// oscillate against a 413).
	if bo.Failures == 0 {
		bo.Limit = min(MaxLimit, bo.Limit*2)
	}
	names, err := u.Outbox.List()
	if err != nil {
		res.Err = err
		return res
	}
	for _, name := range names {
		b, err := u.Outbox.Read(name)
		if err != nil {
			u.Log.Printf("upload: %v", err)
			continue
		}
		stop := u.drain(ctx, deadline, bo, &hb, name, &b, &res)
		if stop {
			return res
		}
	}
	if hb != nil && u.Now().Before(deadline) {
		req := u.request(outbox.Batch{}, hb)
		if _, ok := u.send(ctx, bo, req, &res); ok {
			res.HeartbeatSent = true
		}
	}
	return res
}

// drain uploads one outbox file; it returns true when the pass must stop
// (transport or HTTP error, deadline, ctx). Ids the server keeps answering
// with "retry" are left in the file with their attempt counts and the pass
// moves on to the next file.
func (u *Uploader) drain(ctx context.Context, deadline time.Time, bo *store.Backoff, hb **model.Heartbeat, name string, b *outbox.Batch, res *Result) bool {
	pending := *b
	attempts := pending.Attempts
	if attempts == nil {
		attempts = map[string]int{}
	}
	persisted := pending.Len()
	persist := func() {
		pending.Attempts = nil
		if len(attempts) > 0 {
			pending.Attempts = attempts
		}
		if err := u.Outbox.Rewrite(name, pending); err != nil {
			u.Log.Printf("upload: rewrite %s: %v", name, err)
		} else if u.Progress != nil {
			u.progress.Remaining += pending.Len() - persisted
			persisted = pending.Len()
			u.progress.Accepted = res.Accepted
			u.report()
		}
	}
	stalls := 0
	for pending.Len() > 0 {
		if u.Now().After(deadline) || ctx.Err() != nil {
			persist()
			return true
		}
		part, rest := take(pending, bo.Limit)
		req := u.request(part, *hb)
		resp, ok := u.send(ctx, bo, req, res)
		if !ok {
			code := Code(res.Err)
			if code == 413 {
				// Too large: bisect, and drop a lone item the server still
				// refuses so one record cannot block the queue forever.
				if part.Len() == 1 {
					u.Log.Printf("upload: dropping %s after HTTP %d", onlyID(part), code)
					res.Dropped++
					pending = rest
					res.Err = nil
					persist()
					continue
				}
				bo.Limit = max(1, min(bo.Limit, part.Len())/2)
				res.Err = nil
				continue
			}
			persist()
			return true
		}
		if *hb != nil {
			res.HeartbeatSent = true
			*hb = nil
		}
		sent := map[string]bool{}
		for _, id := range part.IDs() {
			sent[id] = true
		}
		// Ids the server found invalid are logged (id and the server's
		// reason only, never the item) and dead-lettered, never re-sent;
		// they win over "retry".
		rejected := map[string]string{}
		for _, r := range resp.Rejected {
			switch {
			case r.ID == "":
				u.Log.Printf("upload: server rejected the heartbeat: %s", clip(r.Error))
			case sent[r.ID]:
				rejected[r.ID] = clip(r.Error)
				u.Log.Printf("upload: server rejected %s: %s", r.ID, rejected[r.ID])
			}
		}
		retry := map[string]bool{}
		for _, id := range resp.Retry {
			if _, bad := rejected[id]; sent[id] && !bad {
				retry[id] = true
			}
		}
		asked := len(retry)
		res.Accepted += len(sent) - asked - len(rejected)
		res.Rejected += len(rejected)
		// Count this send against every retried id; the others are done.
		for id := range sent {
			if retry[id] {
				attempts[id]++
			} else {
				delete(attempts, id)
			}
		}
		dead := map[string]bool{}
		reasons := map[string]string{}
		for id, e := range rejected {
			dead[id] = true
			reasons[id] = "rejected: " + e
		}
		for id := range retry {
			if attempts[id] >= MaxAttempts {
				dead[id] = true
				reasons[id] = fmt.Sprintf("retry limit: %d attempts", attempts[id])
			}
		}
		if len(dead) > 0 {
			dname, err := u.Outbox.WriteDead(part.Keep(dead), reasons, u.Now())
			if err != nil {
				// The rejected ids are invalid and gone either way; the
				// exhausted ones stay queued rather than get lost.
				u.Log.Printf("upload: dead-letter: %v", err)
				clear(dead)
			} else {
				u.Log.Printf("upload: %d ids moved to %s", len(dead), dname)
				res.DeadLettered += len(dead)
			}
		}
		for id := range dead {
			delete(retry, id)
			delete(attempts, id)
		}
		res.Retried += len(retry)
		again := part.Keep(retry)
		pending = concat(rest, again)
		persist()
		if asked == 0 {
			stalls = 0
			continue
		}
		// The server hit its time budget (or keeps refusing these ids).
		// Re-send soon, but once this file makes no progress twice in a row
		// leave what is left (with its attempt counts) for the next tick and
		// move on, so one stuck id never blocks the files after it.
		if asked == len(sent) {
			stalls++
			if stalls >= 2 {
				return false
			}
		}
		if u.Sleep != nil {
			u.Sleep(time.Second)
		}
	}
	return false
}

// clip shortens a server error string for the log.
func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func (u *Uploader) request(b outbox.Batch, hb *model.Heartbeat) *ingestRequest {
	return newIngestRequest(u.Version, u.Now().UTC(), b, hb, u.Homes)
}

// send posts one request and applies the backoff rules. ok is false on any
// failure; res.Err holds it.
func (u *Uploader) send(ctx context.Context, bo *store.Backoff, req *ingestRequest, res *Result) (ingestResponse, bool) {
	res.Requests++
	start := u.Now()
	u.progress.InFlight = true
	u.report()
	resp, err := u.Client.ingest(ctx, req)
	u.progress.InFlight = false
	u.report()
	end := u.Now()
	if err == nil {
		if !resp.ServerTime.IsZero() {
			mid := start.Add(end.Sub(start) / 2)
			skew := resp.ServerTime.Sub(mid).Milliseconds()
			res.ServerSkewMs = &skew
		}
		bo.Failures = 0
		bo.Until = time.Time{}
		return resp, true
	}
	res.Err = err
	code := Code(err)
	switch {
	case code == 401:
		res.Unauthorized = true
	case code == 413:
		// Handled by the caller (bisect).
	default:
		// 5xx, other statuses (including a 400 schema rejection, which must
		// not drop data), timeouts and network errors.
		bo.Failures++
		if bo.Failures >= 2 {
			bo.Limit = max(MinLimit, bo.Limit/2)
		}
		bo.Until = end.Add(backoffDelay(bo.Failures))
		if errors.Is(err, context.DeadlineExceeded) {
			res.Err = errors.New("upload timed out")
		}
	}
	return resp, false
}

// take splits off one request's worth of items under the per-kind caps,
// limit (items in total), the prompt byte cap and the body cap. It always takes at least one
// item so a single oversized record still gets its chance (and its 413).
func take(b outbox.Batch, limit int) (part, rest outbox.Batch) {
	budget := bodyBudget
	n := 0
	fits := func(v any, size *int) bool {
		s := jsonSize(v)
		if n > 0 && s > budget {
			return false
		}
		*size = s
		return true
	}
	var s int
	i := 0
	for ; i < len(b.Usage) && i < MaxUsage && n < limit; i++ {
		if !fits(b.Usage[i], &s) {
			break
		}
		budget -= s
		n++
	}
	part.Usage, rest.Usage = b.Usage[:i], b.Usage[i:]
	i = 0
	for ; i < len(b.Activity) && i < MaxActivity && n < limit; i++ {
		if !fits(b.Activity[i], &s) {
			break
		}
		budget -= s
		n++
	}
	part.Activity, rest.Activity = b.Activity[:i], b.Activity[i:]
	i = 0
	pbytes := 0
	for ; i < len(b.Prompts) && i < MaxPrompts && n < limit; i++ {
		if !fits(b.Prompts[i], &s) || (n > 0 && pbytes+s > promptBudget) {
			break
		}
		budget -= s
		pbytes += s
		n++
	}
	part.Prompts, rest.Prompts = b.Prompts[:i], b.Prompts[i:]
	i = 0
	for ; i < len(b.Limits) && i < MaxLimits && n < limit; i++ {
		if !fits(b.Limits[i], &s) {
			break
		}
		budget -= s
		n++
	}
	part.Limits, rest.Limits = b.Limits[:i], b.Limits[i:]
	i = 0
	for ; i < len(b.AccountUsage) && i < MaxAccountUsage && n < limit; i++ {
		if !fits(b.AccountUsage[i], &s) {
			break
		}
		budget -= s
		n++
	}
	part.AccountUsage, rest.AccountUsage = b.AccountUsage[:i], b.AccountUsage[i:]
	return part, rest
}

func jsonSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b) + 1
}

func onlyID(b outbox.Batch) string {
	if all := b.IDs(); len(all) > 0 {
		return all[0]
	}
	return ""
}

func concat(a, b outbox.Batch) outbox.Batch {
	return outbox.Batch{
		Usage:        append(append([]model.UsageEvent(nil), a.Usage...), b.Usage...),
		Activity:     append(append([]model.ActivityEvent(nil), a.Activity...), b.Activity...),
		Prompts:      append(append([]model.PromptRecord(nil), a.Prompts...), b.Prompts...),
		Limits:       append(append([]model.LimitSnapshot(nil), a.Limits...), b.Limits...),
		AccountUsage: append(append([]model.AccountUsageSnapshot(nil), a.AccountUsage...), b.AccountUsage...),
	}
}

func (u *Uploader) report() {
	if u.Progress != nil {
		u.Progress(u.progress)
	}
}
