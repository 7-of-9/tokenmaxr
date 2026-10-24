package ghpub

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
)

type readIndex struct {
	Schema   int `json:"schema"`
	Machines []struct {
		ID    string            `json:"id"`
		Files []string          `json:"files"`
		Blobs map[string]string `json:"blobs"`
	} `json:"machines"`
}

func TestDataCommitsKeepTheIndexRight(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	a := Machine{ID: "m_aaaaaaaaaaaa", Label: "desk", OS: "windows", Collector: "0.4.14"}
	b := "data/machines/m_bbbbbbbbbbbb/"
	// Already there: another machine's files, a quota.json an older collector
	// published (never listed), the site, and folders that are not machines.
	f.Files[b+"meta.json"] = `{"id":"m_bbbbbbbbbbbb"}`
	f.Files[b+"usage-2026-09.json"] = `{"rows":[]}`
	f.Files[b+"quota.json"] = `{}`
	f.Files["data/machines/notes/meta.json"] = `{}`
	f.Files["site/index.html"] = `<!doctype html>`

	// Another machine (an older collector, which writes no index) commits
	// between this one's read of the head and its commit: the retry lists it.
	f.Interleave = func(files map[string]string) { files[b+"usage-2026-10.json"] = `{"rows":[[1]]}` }
	ch, err := Publish(ctx, c, "octo/agent-usage", "main", a.Label, Files(a, sampleRows(), AccountHistory{}, now), map[string]string{}, false)
	if err != nil || len(ch) == 0 {
		t.Fatalf("publish: %v %v", ch, err)
	}
	if _, ok := ch[IndexPath]; ok {
		t.Fatal("the index is the repository's, not a file this machine tracks")
	}
	var idx readIndex
	if err := json.Unmarshal([]byte(f.Files[IndexPath]), &idx); err != nil {
		t.Fatalf("index %q: %v", f.Files[IndexPath], err)
	}
	if idx.Schema != IndexSchema || len(idx.Machines) != 2 || idx.Machines[0].ID != a.ID || idx.Machines[1].ID != "m_bbbbbbbbbbbb" {
		t.Fatalf("index %s", f.Files[IndexPath])
	}
	if got := idx.Machines[1].Files; !slices.Equal(got, []string{"meta.json", "usage-2026-09.json", "usage-2026-10.json"}) {
		t.Fatalf("machine b lists %v", got)
	}
	for _, m := range idx.Machines {
		for _, name := range m.Files {
			path := "data/machines/" + m.ID + "/" + name
			if m.Blobs[name] != ghapi.BlobSHA([]byte(f.Files[path])) {
				t.Fatalf("%s: blob %s, content %q", path, m.Blobs[name], f.Files[path])
			}
		}
	}

	// The same files again: Index is deterministic, so a commit that changes
	// nothing listed leaves the index as it is.
	before := f.Files[IndexPath]
	blobs := map[string]string{}
	for p, content := range f.Files {
		blobs[p] = ghapi.BlobSHA([]byte(content))
	}
	if string(Index(blobs)) != before {
		t.Fatalf("index not deterministic:\n%s\n%s", Index(blobs), before)
	}
	if _, ok := indexUpdate(ctx, c, "octo/agent-usage", f.Head, nil); ok {
		t.Fatal("an index that is right was rewritten")
	}
	// A deleted file leaves the list in the same commit.
	if file, ok := indexUpdate(ctx, c, "octo/agent-usage", f.Head, []ghapi.File{{Path: b + "usage-2026-09.json", Delete: true}}); !ok {
		t.Fatal("a deletion did not update the index")
	} else if json.Unmarshal(file.Content, &idx) != nil || slices.Contains(idx.Machines[1].Files, "usage-2026-09.json") {
		t.Fatalf("index after deletion %s", file.Content)
	}
}

func TestBlobSHAIsGitsObjectID(t *testing.T) {
	// `printf 'hello\n' | git hash-object --stdin`
	if got := ghapi.BlobSHA([]byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatal(got)
	}
}
