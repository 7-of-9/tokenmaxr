package ghpub

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
)

// IndexPath is the repository's list of published files. GitHub Pages and
// raw.githubusercontent.com cannot list folders, so the dashboard reads this
// to know which machines and files there are. Every data commit rewrites it
// from the branch it lands on, so it stays right without a Pages deploy: the
// dashboard of a public repository reads it, and the files it lists, straight
// from the repository (pages/dashboard/githubSource.ts). The Pages workflow
// writes its own copy into the deployed site (scripts/build-index.mjs, the
// same format with the title and build time), which a private repository's
// dashboard reads instead.
const IndexPath = "data/index.json"

// IndexSchema 4 adds "blobs" (each file's git object id, so the dashboard
// reads again only the files that changed) to schema 3 (scripts/build-index.mjs:
// meta, account-usage, owner and usage-YYYY-MM files).
const IndexSchema = 4

var (
	indexMachine = regexp.MustCompile(`^m_[0-9a-f]{12}$`)
	// quota.json (published in the clear before collector 0.4.2) is not
	// listed: the dashboard does not read it, and Files deletes it.
	indexFileName = regexp.MustCompile(`^(meta|account-usage|owner)\.json$|^usage-\d{4}-\d{2}\.json$`)
)

type indexFile struct {
	Schema   int                 `json:"schema"`
	Machines []indexMachineFiles `json:"machines"`
}

type indexMachineFiles struct {
	ID    string            `json:"id"`
	Files []string          `json:"files"`
	Blobs map[string]string `json:"blobs"`
}

// Index is data/index.json for the files in blobs (path -> git object id):
// every machine folder with at least one listed file, sorted. It has no time
// in it, so the same files give the same bytes.
func Index(blobs map[string]string) []byte {
	byMachine := map[string]*indexMachineFiles{}
	for path, sha := range blobs {
		rest, ok := strings.CutPrefix(path, "data/machines/")
		id, name, ok2 := strings.Cut(rest, "/")
		if !ok || !ok2 || !indexMachine.MatchString(id) || !indexFileName.MatchString(name) {
			continue
		}
		m := byMachine[id]
		if m == nil {
			m = &indexMachineFiles{ID: id, Blobs: map[string]string{}}
			byMachine[id] = m
		}
		m.Files = append(m.Files, name)
		m.Blobs[name] = sha
	}
	idx := indexFile{Schema: IndexSchema, Machines: []indexMachineFiles{}}
	for _, id := range slices.Sorted(maps.Keys(byMachine)) {
		m := byMachine[id]
		slices.Sort(m.Files)
		idx.Machines = append(idx.Machines, *m)
	}
	return marshal(idx)
}

// indexUpdate is the data/index.json that a commit of files on top of head
// leaves right, or false when the one there already is (or the branch cannot
// be listed: the data is committed all the same, and the next commit lists
// it again).
func indexUpdate(ctx context.Context, c *ghapi.Client, repo, head string, files []ghapi.File) (ghapi.File, bool) {
	entries, err := c.Tree(ctx, repo, head)
	if err != nil {
		return ghapi.File{}, false
	}
	blobs := map[string]string{}
	current := ""
	for _, e := range entries {
		if e.Path == IndexPath {
			current = e.SHA
			continue
		}
		blobs[e.Path] = e.SHA
	}
	for _, f := range files {
		if f.Delete {
			delete(blobs, f.Path)
		} else {
			blobs[f.Path] = ghapi.BlobSHA(f.Content)
		}
	}
	b := Index(blobs)
	if ghapi.BlobSHA(b) == current {
		return ghapi.File{}, false
	}
	return ghapi.File{Path: IndexPath, Content: b}, true
}
