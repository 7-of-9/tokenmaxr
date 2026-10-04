package evidence

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const xai = model.ProviderXAI

// grokRow is the only shape decoded from logs/unified.jsonl. ctx also
// carries token prefixes; they are never decoded.
type grokRow struct {
	TS  string          `json:"ts"`
	PID json.RawMessage `json:"pid"`
	SID string          `json:"sid"`
	Msg string          `json:"msg"`
	Ctx struct {
		UserID string `json:"user_id"`
		Method string `json:"method"`
	} `json:"ctx"`
}

// grok harvests unified.jsonl: user_info per process (G1), which session
// ran in which process (the pid->sid join), and interactive logins (the
// token chain's boundaries, G3).
func (h *harvester) grok() bool {
	path := filepath.Join(h.o.Home, ".grok", "logs", "unified.jsonl")
	st, changed := h.changed(path)
	if !changed {
		return true
	}
	m := h.marks[path]
	if st.Size() < m.Off {
		m = Mark{} // rotated
	}
	r, err := jsonl.Open(path, m.Off)
	if err != nil {
		return true
	}
	defer r.Close()
	type key struct{ sid, proc string }
	spans := map[key]*Record{}
	var order []key
	for {
		b, ok, err := r.Next()
		if err != nil || !ok {
			break
		}
		var row grokRow
		if !jsonl.Decode(b, &row) {
			continue
		}
		t, okTS := tsOf(row.TS)
		if !okTS {
			continue
		}
		proc := "pid:" + strings.Trim(string(row.PID), `" `)
		if row.SID != "" && len(row.PID) > 0 {
			k := key{row.SID, proc}
			s := spans[k]
			if s == nil {
				s = &Record{Provider: xai, Kind: KindSpan, Source: SrcGrokSession, Stream: session(xai, row.SID), Proc: proc, TS: t, To: t}
				spans[k] = s
				order = append(order, k)
			}
			if t.Before(s.TS) {
				s.TS = t
			}
			if t.After(s.To) {
				s.To = t
			}
		}
		switch row.Msg {
		case "auth init user_info check":
			if a := h.hash(xai, row.Ctx.UserID); a != "" {
				h.out(Record{Provider: xai, Kind: KindSample, Source: SrcGrokUserInfo, Q: QExact, Acct: a, Proc: proc, TS: t})
			}
		case "auth started":
			// cached_token reuses the stored login; anything else is an
			// interactive login, which may switch the account.
			if mt := strings.TrimSpace(row.Ctx.Method); mt != "" && mt != "cached_token" {
				h.out(Record{Provider: xai, Kind: KindBoundary, Source: SrcGrokLogin, Proc: proc, TS: t})
			}
		}
	}
	for _, k := range order {
		h.out(*spans[k])
	}
	m.Off, m.Size, m.MtimeNs = r.Offset(), st.Size(), st.ModTime().UnixNano()
	h.marks[path] = m
	h.res.Files++
	return true
}
