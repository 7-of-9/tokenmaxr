package evidence

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
)

const retrievalLog = "Cursor Indexing & Retrieval.log"

var (
	cursorFolder = regexp.MustCompile(`^\d{8}T\d{6}$`)
	// The retrieval extension logs its repo watcher's owner: the signed-in
	// user's id (provider|user_...), in local time.
	ownerLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?) .*\bowner: ([A-Za-z0-9_-]+\|[A-Za-z0-9_-]+)`)
)

// CursorLogsDir is Cursor's logs folder (one sub-folder per app session).
func CursorLogsDir(home string) string {
	return filepath.Join(filepath.Dir(cursor.UserDir(home)), "logs")
}

// cursor harvests K1: each log folder is a window from its start (the
// folder name, local time) to its last file write; the retrieval log's
// owner lines name the account inside it.
func (h *harvester) cursor() bool {
	root := h.o.CursorLogs
	if root == "" {
		root = CursorLogsDir(h.o.Home)
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return true
	}
	for _, d := range dirs {
		if !d.IsDir() || !cursorFolder.MatchString(d.Name()) {
			continue
		}
		start, err := time.ParseInLocation("20060102T150405", d.Name(), time.Local)
		if err != nil {
			continue
		}
		dir := filepath.Join(root, d.Name())
		var end time.Time
		var logs []string
		var size int64
		filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if info, err := e.Info(); err == nil {
				size += info.Size()
				if info.ModTime().After(end) {
					end = info.ModTime()
				}
			}
			if e.Name() == retrievalLog {
				logs = append(logs, p)
			}
			return nil
		})
		m, ok := h.marks[dir]
		if ok && m.Size == size && m.MtimeNs == end.UnixNano() {
			continue
		}
		if h.late() {
			return false
		}
		owners := map[string]bool{}
		var lastOwner string
		proc := "log:" + d.Name()
		for _, p := range logs {
			f, err := fsx.Open(p)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 4<<20)
			for sc.Scan() {
				mm := ownerLine.FindSubmatch(sc.Bytes())
				if mm == nil {
					continue
				}
				t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", string(mm[1]), time.Local)
				if err != nil {
					continue
				}
				a := h.hash(cursor.ProviderName, string(mm[2]))
				if a == "" {
					continue
				}
				owners[a] = true
				lastOwner = a
				h.out(Record{Provider: cursor.ProviderName, Kind: KindSample, Source: SrcCursorRetrieval, Q: QExact, Acct: a, Proc: proc, TS: t.UTC()})
			}
			f.Close()
		}
		if len(owners) > 0 {
			w := Record{Provider: cursor.ProviderName, Kind: KindWindow, Source: SrcCursorRetrieval, Proc: proc, TS: start.UTC(), To: end.UTC()}
			if len(owners) == 1 {
				w.Acct = lastOwner
			}
			h.out(w)
		}
		h.marks[dir] = Mark{Size: size, MtimeNs: end.UnixNano()}
		h.res.Files++
	}
	return true
}
