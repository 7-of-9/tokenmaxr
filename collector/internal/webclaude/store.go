package webclaude

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
)

// Files inside the web-claude directory (paths.WebClaude).
func SpoolDir(dir string) string     { return filepath.Join(dir, "spool") }
func settingsPath(dir string) string { return filepath.Join(dir, "settings.json") }
func enrolledPath(dir string) string { return filepath.Join(dir, "enrolled.json") }
func helloPath(dir string) string    { return filepath.Join(dir, "hello.json") }
func claimsPath(dir string) string   { return filepath.Join(dir, "claims.txt") }
func pendingPath(dir string) string  { return filepath.Join(dir, "pending.txt") }
func lockPath(dir string) string     { return filepath.Join(dir, "spool.lock") }
func LogPath(dir string) string      { return filepath.Join(dir, "host.log") }

// SpoolRetention: the host deletes spool days older than this. The
// collector reads a day within a minute of it being written.
const SpoolRetention = 45 * 24 * time.Hour

// DefaultSettle is how old a turn must be before the sweeper reports it:
// longer than hourly publishing, so other machines' live records have landed.
const DefaultSettle = 6 * time.Hour

// Settings are the owner's Claude Web choices for this machine.
type Settings struct {
	// Sweeper: this machine's browser reads the account's conversation
	// history (estimated). Enable it on one machine only.
	Sweeper bool `json:"sweeper"`
	// InferApps classifies swept turns after T0 that no live record
	// claimed as Claude Native (apps). Nil means on. Turn it off when other
	// machines capture claude.ai and publish only to GitHub (their claims
	// are not visible here).
	InferApps *bool `json:"inferApps,omitempty"`
	// SettleMinutes overrides DefaultSettle (tests); 0 means the default,
	// negative means no wait.
	SettleMinutes int `json:"settleMinutes,omitempty"`
	// Prompts is Claude chat prompt text capture, on top of the collector's
	// own prompt capture (both must be on): "" or PromptsOff keeps no chat
	// text, PromptsLive keeps the text of turns typed in this browser, and
	// PromptsAll also that of the turns the history sweep reads.
	Prompts string `json:"prompts,omitempty"`
	// ChatBefore is the start of Claude Web capture across the fleet, when
	// it is earlier than this machine's T0: swept turns before it are
	// chat-claude, and turns from it to T0 are skipped, since another
	// machine's browser may have recorded them (SPEC "Claude chat").
	ChatBefore time.Time `json:"chatBefore,omitzero"`
}

// Prompt text settings (Settings.Prompts).
const (
	PromptsOff  = "off"
	PromptsLive = "live"
	PromptsAll  = "all"
)

// PromptMode is Prompts normalised to off, live or all.
func (s Settings) PromptMode() string {
	switch s.Prompts {
	case PromptsLive, PromptsAll:
		return s.Prompts
	}
	return PromptsOff
}

// TextAllowed reports whether chat prompt text of a record kind may be kept
// (the collector's prompt capture must be on as well).
func (s Settings) TextAllowed(kind string) bool {
	switch s.PromptMode() {
	case PromptsAll:
		return kind == KindLive || kind == KindSweep
	case PromptsLive:
		return kind == KindLive
	}
	return false
}

// HistoryCut is when swept turns stop being chat-claude: ChatBefore when it
// is set and earlier than t0, else t0.
func (s Settings) HistoryCut(t0 time.Time) time.Time {
	if !s.ChatBefore.IsZero() && (t0.IsZero() || s.ChatBefore.Before(t0)) {
		return s.ChatBefore.UTC()
	}
	return t0
}

func (s Settings) Infer() bool { return s.InferApps == nil || *s.InferApps }

func (s Settings) Settle() time.Duration {
	switch {
	case s.SettleMinutes < 0:
		return 0
	case s.SettleMinutes == 0:
		return DefaultSettle
	}
	return time.Duration(s.SettleMinutes) * time.Minute
}

// Enrolled is when this machine's capture first connected (T0): from then
// on, a turn typed in this browser is recorded live.
type Enrolled struct {
	T0 time.Time `json:"t0"`
}

// Hello is the host's status file, written on every connection.
type Hello struct {
	Ext          string    `json:"ext,omitempty"`
	Origin       string    `json:"origin,omitempty"`
	ConnectedAt  time.Time `json:"connectedAt"`
	LastRecordAt time.Time `json:"lastRecordAt,omitzero"`
	Day          string    `json:"day,omitempty"`
	RecordsToday int       `json:"recordsToday"`
	Records      int64     `json:"records"`
	Rejected     int64     `json:"rejected"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON replaces path atomically, owner-only.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadSettings(dir string) Settings {
	var s Settings
	_ = readJSON(settingsPath(dir), &s)
	return s
}

func SaveSettings(dir string, s Settings) error { return writeJSON(settingsPath(dir), s) }

func LoadEnrolled(dir string) Enrolled {
	var e Enrolled
	_ = readJSON(enrolledPath(dir), &e)
	return e
}

// Enrol records T0 the first time and returns it.
func Enrol(dir string, now time.Time) (time.Time, error) {
	if e := LoadEnrolled(dir); !e.T0.IsZero() {
		return e.T0, nil
	}
	t0 := now.UTC().Truncate(time.Second)
	return t0, writeJSON(enrolledPath(dir), Enrolled{T0: t0})
}

func LoadHello(dir string) (Hello, bool) {
	var h Hello
	err := readJSON(helloPath(dir), &h)
	return h, err == nil
}

func SaveHello(dir string, h Hello) error { return writeJSON(helloPath(dir), h) }

// Lock serialises writers of the spool, claims and status files (several
// host processes can run at once: one per browser message).
func Lock(dir string) (*lock.Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return lock.Acquire(lockPath(dir), 10*time.Second)
}

// Append writes encoded records to today's spool file and the usage ids of
// live records to the claims file. The caller holds Lock.
func Append(dir string, now time.Time, lines [][]byte, claims []string) error {
	if len(lines) > 0 {
		sd := SpoolDir(dir)
		if err := os.MkdirAll(sd, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(sd, now.UTC().Format("2006-01-02")+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		var buf []byte
		for _, l := range lines {
			buf = append(append(buf, l...), '\n')
		}
		_, err = f.Write(buf)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if len(claims) > 0 {
		f, err := os.OpenFile(claimsPath(dir), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(strings.Join(claims, "\n") + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Prune deletes spool days older than SpoolRetention. The caller holds Lock.
func Prune(dir string, now time.Time) {
	files, _ := SpoolFiles(dir)
	cut := now.UTC().Add(-SpoolRetention).Format("2006-01-02")
	for _, f := range files {
		if day := strings.TrimSuffix(filepath.Base(f), ".jsonl"); day < cut {
			_ = os.Remove(f)
		}
	}
}

// SpoolFiles lists the spool's day files, oldest first.
func SpoolFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(SpoolDir(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".jsonl") && len(n) == len("2006-01-02.jsonl") {
			out = append(out, filepath.Join(SpoolDir(dir), n))
		}
	}
	sort.Strings(out)
	return out, nil
}

// idCache caches a file of "<usage id>[ <value>]" lines, which only grows,
// by size and modification time.
type idCache struct {
	sync.Mutex
	path    string
	size    int64
	mtimeNs int64
	set     map[string]string
}

var claimCache, pendingCache idCache

func (c *idCache) load(p string) map[string]string {
	st, err := os.Stat(p)
	if err != nil {
		return map[string]string{}
	}
	c.Lock()
	defer c.Unlock()
	if c.path == p && c.size == st.Size() && c.mtimeNs == st.ModTime().UnixNano() {
		return c.set
	}
	set := map[string]string{}
	if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			id, val, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
			if len(id) == 32 {
				set[id] = val
			}
		}
		f.Close()
	}
	c.path, c.size, c.mtimeNs, c.set = p, st.Size(), st.ModTime().UnixNano(), set
	return set
}

// Claims is the set of canonical usage ids recorded live on this machine.
func Claims(dir string) map[string]bool {
	set := map[string]bool{}
	for id := range claimCache.load(claimsPath(dir)) {
		set[id] = true
	}
	return set
}

// Pending is a turn the extension saw end in this browser, sent as soon as
// its stream ended (kind "claim"): the usage id and the client platform. Its
// live record follows a few seconds later, unless the tab closed first; a
// swept record of a pending turn then reports it in the client's channel
// (internal/sources/claudeweb).
type Pending struct {
	ID       string
	Platform string
}

// AppendPending records pending turns. The caller holds Lock.
func AppendPending(dir string, ps []Pending) error {
	if len(ps) == 0 {
		return nil
	}
	var b strings.Builder
	for _, p := range ps {
		plat := p.Platform
		if plat == "" {
			plat = "-"
		}
		b.WriteString(p.ID + " " + plat + "\n")
	}
	f, err := os.OpenFile(pendingPath(dir), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(b.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// PendingClaims maps the usage ids of pending turns to their client
// platform ("" when the request did not name one).
func PendingClaims(dir string) map[string]string {
	out := map[string]string{}
	for id, plat := range pendingCache.load(pendingPath(dir)) {
		if plat == "-" {
			plat = ""
		}
		out[id] = plat
	}
	return out
}
