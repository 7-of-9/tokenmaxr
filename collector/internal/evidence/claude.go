package evidence

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

const anthropic = model.ProviderAnthropic

var (
	keyCredOrg = []byte(`"credential_org"`)
	keyBridge  = []byte(`"bridge-session"`)
	keyLedger  = []byte(`"artifact-autoreact-ledger"`)
	keyStamp   = []byte(`"timestamp":"`)
	keyLogin   = []byte(`"/log`)
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// claudeIdent is the only shape decoded from a transcript line: the record
// type, its time and its identity fields.
type claudeIdent struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	SessionID  string `json:"sessionId"`
	Attachment *struct {
		Type             string `json:"type"`
		OrganizationUUID string `json:"organizationUuid"`
	} `json:"attachment"`
	OwnerAccountUUID      string `json:"ownerAccountUuid"`
	OwnerOrganizationUUID string `json:"ownerOrganizationUuid"`
	AccountUUID           string `json:"accountUuid"`
}

// oauthSnapshot is the identity part of a .claude.json (or a backup of it);
// tokens and every other field are never decoded.
type oauthSnapshot struct {
	OAuthAccount *struct {
		AccountUUID      string `json:"accountUuid"`
		OrganizationUUID string `json:"organizationUuid"`
		EmailAddress     string `json:"emailAddress"`
		OrganizationName string `json:"organizationName"`
	} `json:"oauthAccount"`
}

func (h *harvester) claude() bool {
	dir := filepath.Join(h.o.Home, ".claude")
	if !h.claudeTranscripts(filepath.Join(dir, "projects")) {
		return false
	}
	h.claudeHistory(filepath.Join(dir, "history.jsonl"))
	// Snapshots: rolling backups, the one-off .claude.json.backup and the
	// live file (its account is also the probe's; here it feeds the org map).
	if ents, err := os.ReadDir(filepath.Join(dir, "backups")); err == nil {
		for _, e := range ents {
			if e.Type().IsRegular() && strings.Contains(e.Name(), ".claude.json") {
				h.claudeSnapshot(filepath.Join(dir, "backups", e.Name()), SrcClaudeBackup, true)
			}
		}
	}
	h.claudeSnapshot(filepath.Join(h.o.Home, ".claude.json.backup"), SrcClaudeJSONBak, true)
	h.claudeSnapshot(filepath.Join(h.o.Home, ".claude.json"), SrcClaudeJSON, false)
	h.claudeDesktop()
	return true
}

func transcriptFiles(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
	return out
}

// claudeTranscripts harvests credential_org, bridge-session and
// autoreact-ledger records. The latter two carry no timestamp: their time is
// the previous timestamped line's.
func (h *harvester) claudeTranscripts(root string) bool {
	for _, path := range transcriptFiles(root) {
		st, changed := h.changed(path)
		if !changed {
			continue
		}
		if h.late() {
			return false
		}
		m := h.marks[path]
		if st.Size() < m.Off {
			m = Mark{}
		}
		if err := h.claudeTranscript(path, &m); err != nil {
			continue
		}
		m.Size, m.MtimeNs = st.Size(), st.ModTime().UnixNano()
		h.marks[path] = m
		h.res.Files++
	}
	return true
}

func (h *harvester) claudeTranscript(path string, m *Mark) error {
	sameAcct := func(a, b Record) bool { return a.Acct == b.Acct && a.Org == b.Org && a.Stream == b.Stream }
	// Pass 1 decodes only credential_org lines, and notes whether the read
	// holds records without a timestamp of their own.
	r, err := jsonl.Open(path, m.Off)
	if err != nil {
		return err
	}
	var cred run
	var stamped []byte
	slow := false
	for {
		b, ok, err := r.Next()
		if err != nil {
			r.Close()
			return err
		}
		if !ok {
			break
		}
		if bytes.Contains(b, keyBridge) || bytes.Contains(b, keyLedger) {
			slow = true
		}
		if bytes.Contains(b, keyStamp) {
			stamped = append(stamped[:0], b...)
		}
		if !bytes.Contains(b, keyCredOrg) {
			continue
		}
		var l claudeIdent
		if !jsonl.Decode(b, &l) || l.Type != "attachment" || l.Attachment == nil || l.Attachment.Type != "credential_org" {
			continue
		}
		if t, ok := tsOf(l.Timestamp); ok && l.Attachment.OrganizationUUID != "" {
			cred.add(h, Record{Provider: anthropic, Kind: KindSample, Source: SrcClaudeCredOrg, Q: QExact,
				Org: h.hashOrg(anthropic, l.Attachment.OrganizationUUID), Stream: session(anthropic, l.SessionID), TS: t}, sameAcct)
		}
	}
	end := r.Offset()
	r.Close()
	cred.flush(h)
	lastTS := m.Last
	var p struct {
		Timestamp string `json:"timestamp"`
	}
	if len(stamped) > 0 && jsonl.Decode(stamped, &p) {
		if t, ok := tsOf(p.Timestamp); ok {
			lastTS = t
		}
	}
	if slow {
		// Pass 2: bridge-session and autoreact-ledger lines take the time
		// of the last line before them that has one.
		if err := h.claudeUntimed(path, m.Off, end, m.Last, sameAcct); err != nil {
			return err
		}
	}
	m.Off, m.Last = end, lastTS
	return nil
}

func (h *harvester) claudeUntimed(path string, from, to int64, lastTS time.Time, same func(a, b Record) bool) error {
	r, err := jsonl.Open(path, from)
	if err != nil {
		return err
	}
	defer r.Close()
	var bridge, ledger run
	for r.Offset() < to {
		b, ok, err := r.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		isBridge, isLedger := bytes.Contains(b, keyBridge), bytes.Contains(b, keyLedger)
		if !isBridge && !isLedger && !bytes.Contains(b, keyStamp) {
			continue
		}
		var l claudeIdent
		if !jsonl.Decode(b, &l) {
			continue
		}
		if t, ok := tsOf(l.Timestamp); ok {
			lastTS = t
		}
		switch {
		case l.Type == "bridge-session" && l.OwnerAccountUUID != "":
			acct, org := h.hash(anthropic, l.OwnerAccountUUID), h.hashOrg(anthropic, l.OwnerOrganizationUUID)
			if org != "" {
				h.out(Record{Provider: anthropic, Kind: KindOrgMap, Source: SrcClaudeBridge, Org: org, Acct: acct})
			}
			if !lastTS.IsZero() {
				bridge.add(h, Record{Provider: anthropic, Kind: KindSample, Source: SrcClaudeBridge, Q: QExact, Acct: acct, TS: lastTS}, same)
			}
		case l.Type == "artifact-autoreact-ledger" && l.AccountUUID != "" && !lastTS.IsZero():
			ledger.add(h, Record{Provider: anthropic, Kind: KindSample, Source: SrcClaudeLedger, Q: QExact,
				Acct: h.hash(anthropic, l.AccountUUID), TS: lastTS}, same)
		}
	}
	bridge.flush(h)
	ledger.flush(h)
	return nil
}

// claudeHistory harvests /login and /logout lines of history.jsonl as
// boundaries: they name no identity, they only split intervals.
func (h *harvester) claudeHistory(path string) {
	st, changed := h.changed(path)
	if !changed {
		return
	}
	m := h.marks[path]
	if st.Size() < m.Off {
		m = Mark{}
	}
	r, err := jsonl.Open(path, m.Off)
	if err != nil {
		return
	}
	defer r.Close()
	for {
		b, ok, err := r.Next()
		if err != nil || !ok {
			break
		}
		if !bytes.Contains(b, keyLogin) {
			continue
		}
		var l struct {
			Display   string `json:"display"`
			Timestamp int64  `json:"timestamp"`
		}
		if !jsonl.Decode(b, &l) || l.Timestamp <= 0 {
			continue
		}
		d := strings.TrimSpace(l.Display)
		if cmd, _, _ := strings.Cut(d, " "); cmd == "/login" || cmd == "/logout" {
			h.out(Record{Provider: anthropic, Kind: KindBoundary, Source: SrcClaudeLogin, TS: time.UnixMilli(l.Timestamp).UTC()})
		}
	}
	m.Off, m.Size, m.MtimeNs = r.Offset(), st.Size(), st.ModTime().UnixNano()
	h.marks[path] = m
	h.res.Files++
}

// claudeSnapshot reads the oauthAccount of a .claude.json-shaped file. A
// backup is a sample at its mtime (state after the write); every snapshot
// feeds the org map and the default labels.
func (h *harvester) claudeSnapshot(path, src string, sample bool) {
	st, changed := h.changed(path)
	if !changed {
		return
	}
	b, err := jsonl.ReadFile(path)
	if err != nil {
		return
	}
	h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Off: st.Size()}
	h.res.Files++
	var f oauthSnapshot
	if !jsonl.Decode(b, &f) || f.OAuthAccount == nil || f.OAuthAccount.AccountUUID == "" {
		return
	}
	a := f.OAuthAccount
	acct := h.hash(anthropic, a.AccountUUID)
	if org := h.hashOrg(anthropic, a.OrganizationUUID); org != "" {
		h.out(Record{Provider: anthropic, Kind: KindOrgMap, Source: src, Org: org, Acct: acct})
	}
	h.label(acct, a.EmailAddress, a.OrganizationName)
	if sample {
		h.out(Record{Provider: anthropic, Kind: KindSample, Source: src, Q: QStrong, Acct: acct, TS: st.ModTime().UTC()})
	}
}

// ClaudeDesktopDir is the Claude desktop app's claude-code-sessions folder
// for a home: <account uuid>/<org uuid>/<session>.json.
func ClaudeDesktopDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Claude", "claude-code-sessions")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions")
	}
	return filepath.Join(home, ".config", "Claude", "claude-code-sessions")
}

// claudeDesktop: each session file is a sample of its folder's account from
// createdAt to lastActivityAt, and the folder pair maps org to account.
func (h *harvester) claudeDesktop() {
	root := h.o.ClaudeDesktop
	if root == "" {
		root = ClaudeDesktopDir(h.o.Home)
	}
	accts, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, ad := range accts {
		if !ad.IsDir() || !uuidRe.MatchString(ad.Name()) {
			continue
		}
		orgs, err := os.ReadDir(filepath.Join(root, ad.Name()))
		if err != nil {
			continue
		}
		acct := h.hash(anthropic, ad.Name())
		for _, od := range orgs {
			if !od.IsDir() || !uuidRe.MatchString(od.Name()) {
				continue
			}
			h.out(Record{Provider: anthropic, Kind: KindOrgMap, Source: SrcClaudeDesktop, Org: h.hashOrg(anthropic, od.Name()), Acct: acct})
			dir := filepath.Join(root, ad.Name(), od.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				path := filepath.Join(dir, f.Name())
				st, changed := h.changed(path)
				if !changed {
					continue
				}
				b, err := jsonl.ReadFile(path)
				if err != nil {
					continue
				}
				h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Off: st.Size()}
				h.res.Files++
				var s struct {
					CreatedAt      json.Number `json:"createdAt"`
					LastActivityAt json.Number `json:"lastActivityAt"`
				}
				if !jsonl.Decode(b, &s) {
					continue
				}
				from, to := msTime(s.CreatedAt), msTime(s.LastActivityAt)
				if from.IsZero() {
					from = to
				}
				if from.IsZero() {
					continue
				}
				h.out(Record{Provider: anthropic, Kind: KindSample, Source: SrcClaudeDesktop, Q: QStrong, Acct: acct, TS: from, To: to})
			}
		}
	}
}

func msTime(n json.Number) time.Time {
	v, err := n.Float64()
	if err != nil || v <= 0 {
		return time.Time{}
	}
	if v < 1e11 {
		v *= 1000
	}
	return time.UnixMilli(int64(v)).UTC()
}
