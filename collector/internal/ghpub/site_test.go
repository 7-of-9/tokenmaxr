package ghpub

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
)

func TestEmbeddedSiteIsStampedAndCommittable(t *testing.T) {
	v, files, err := Site()
	if err != nil {
		t.Fatal(err)
	}
	if v.BuiltAt.IsZero() || len(v.Hash) != 64 {
		t.Fatalf("version %+v", v)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		if !strings.HasPrefix(f.Path, "site/") {
			t.Errorf("%s is outside site/", f.Path)
		}
		// Commit sends contents as JSON strings: a binary file would arrive mangled.
		if !utf8.Valid(f.Content) {
			t.Errorf("%s is not UTF-8 text: the dashboard build must not emit binary files", f.Path)
		}
		if strings.Contains(string(f.Content), "\r\n") && strings.HasSuffix(f.Path, ".html") {
			t.Errorf("%s has CRLF line endings (a checkout converted it?)", f.Path)
		}
	}
	if !slices.Contains(paths, "site/index.html") || !slices.Contains(paths, SiteVersionFile) {
		t.Fatalf("embedded site lacks index.html or version.json: %v", paths)
	}
}

// The embedded copy is the build in pages/site (the template's dashboard):
// `npm run build:pages` writes both, and this fails if only one was updated.
func TestEmbeddedSiteMatchesPagesSite(t *testing.T) {
	dir := "../../../pages/site"
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no pages/site beside this module")
	}
	_, files, err := Site()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{}
	for _, f := range files {
		want[strings.TrimPrefix(f.Path, "site/")] = f.Content
	}
	got := map[string][]byte{}
	fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			got[p], _ = os.ReadFile(path.Join(dir, p))
		}
		return err
	})
	for p, b := range want {
		if g, ok := got[p]; !ok || string(g) != string(b) {
			t.Errorf("pages/site/%s differs from the embedded copy: run npm run build:pages", p)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("pages/site/%s is not embedded: run npm run build:pages", p)
		}
	}
}

func TestSiteRejectsAVersionThatDoesNotMatchItsFiles(t *testing.T) {
	files := []ghapi.File{{Path: "site/index.html", Content: []byte("<p>1</p>")}}
	v := SiteVersion{BuiltAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Hash: SiteHash(files)}
	vb, _ := json.Marshal(v)
	fsys := fstest.MapFS{"site/index.html": {Data: []byte("<p>1</p>")}, "site/version.json": {Data: vb}}
	if got, _, err := siteFiles(fsys); err != nil || got != v {
		t.Fatalf("valid site: %+v %v", got, err)
	}
	fsys["site/index.html"] = &fstest.MapFile{Data: []byte("<p>edited</p>")}
	if _, _, err := siteFiles(fsys); err == nil {
		t.Fatal("a hand-edited file must not pass as the stamped build")
	}
	delete(fsys, "site/version.json")
	if _, _, err := siteFiles(fsys); err == nil {
		t.Fatal("no version.json must be an error")
	}
}

// fakeSite is a dashboard build stamped at builtAt with the given files.
func fakeSite(builtAt time.Time, files map[string]string) (SiteVersion, []ghapi.File) {
	var out []ghapi.File
	for _, p := range slices.Sorted(maps.Keys(files)) {
		out = append(out, ghapi.File{Path: p, Content: []byte(files[p])})
	}
	v := SiteVersion{BuiltAt: builtAt, Hash: SiteHash(out)}
	b, _ := json.MarshalIndent(v, "", " ")
	return v, append(out, ghapi.File{Path: SiteVersionFile, Content: append(b, '\n')})
}

// seedRepo is a usage repository created from an old template: the first
// dashboard (no version.json) beside data and the workflow.
func seedRepo(f *ghapitest.Fake) map[string]string {
	keep := map[string]string{
		MarkerFile:                               `{"tokenmaxr":1}`,
		".github/workflows/pages.yml":            "name: dashboard\n",
		"scripts/build-index.mjs":                "// index\n",
		"data/machines/m_abc/meta.json":          `{"id":"m_abc"}`,
		"data/machines/m_abc/usage-2026-10.json": `{"rows":[]}`,
		"README.md":                              "# usage\n",
	}
	for p, c := range keep {
		f.Files[p] = c
	}
	f.Files["site/index.html"] = `<script src="app.js"></script>`
	f.Files["site/app.js"] = "old app"
	f.Files["site/style.css"] = "old style"
	return keep
}

func TestPublishSiteReplacesAnOlderDashboardAndNothingElse(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	keep := seedRepo(f)
	t1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	v1, files1 := fakeSite(t1, map[string]string{"site/index.html": "<p>v1</p>", "site/assets/index-a.js": "a()"})

	// No version.json (the first template): replaced, old files deleted.
	head := f.Head
	updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v1, files1)
	if err != nil || !updated || f.Commits != 1 {
		t.Fatalf("first update: %v %v (%d commits)", updated, err, f.Commits)
	}
	for _, fl := range files1 {
		if f.Files[fl.Path] != string(fl.Content) {
			t.Errorf("%s = %q, want the embedded content", fl.Path, f.Files[fl.Path])
		}
	}
	for _, gone := range []string{"site/app.js", "site/style.css"} {
		if _, ok := f.Files[gone]; ok {
			t.Errorf("%s survived the update", gone)
		}
	}
	for p, c := range keep {
		if f.Files[p] != c {
			t.Errorf("%s changed: %q", p, f.Files[p])
		}
	}
	if len(f.Files) != len(keep)+len(files1) {
		t.Fatalf("unexpected files: %v", slices.Sorted(maps.Keys(f.Files)))
	}
	if f.Reads[len(f.Reads)-1] != SiteVersionFile+"@"+head {
		t.Fatalf("version.json must be read at the publishing branch's head commit (%s): %v", head, f.Reads)
	}

	// Same version again (this machine next tick, or a second machine on the
	// same release): nothing is committed.
	if updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v1, files1); err != nil || updated || f.Commits != 1 {
		t.Fatalf("same version: %v %v (%d commits)", updated, err, f.Commits)
	}

	// A newer build replaces v1 and deletes the asset it no longer has, even
	// after another machine moved the branch once.
	t2 := t1.Add(48 * time.Hour)
	v2, files2 := fakeSite(t2, map[string]string{"site/index.html": "<p>v2</p>", "site/assets/index-b.js": "b()"})
	f.MoveOnce = true
	if updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v2, files2); err != nil || !updated || f.Commits != 2 {
		t.Fatalf("newer version: %v %v (%d commits)", updated, err, f.Commits)
	}
	if _, ok := f.Files["site/assets/index-a.js"]; ok || f.Files["site/assets/index-b.js"] != "b()" || f.Files["site/index.html"] != "<p>v2</p>" {
		t.Fatalf("v2 not in place: %v", f.Files)
	}

	// An older collector (still carrying v1) never downgrades the dashboard.
	if updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v1, files1); err != nil || updated || f.Commits != 2 {
		t.Fatalf("older version: %v %v (%d commits)", updated, err, f.Commits)
	}
	if f.Files["site/index.html"] != "<p>v2</p>" {
		t.Fatal("the older collector downgraded the dashboard")
	}
	for p, c := range keep {
		if f.Files[p] != c {
			t.Errorf("%s changed: %q", p, f.Files[p])
		}
	}
}

func TestPublishSiteLeavesAnUnknownVersionAlone(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	seedRepo(f)
	v, files := fakeSite(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), map[string]string{"site/index.html": "<p>v</p>"})
	for _, unknown := range []string{`{"version":3}`, `not json`, `{"builtAt":"2026-10-05T00:00:00Z"}`} {
		f.Files[SiteVersionFile] = unknown
		if updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v, files); err != nil || updated || f.Commits != 0 {
			t.Fatalf("%s: %v %v", unknown, updated, err)
		}
	}
	// Files outside site/ are refused outright.
	bad := append(slices.Clone(files), ghapi.File{Path: "data/x.json", Content: []byte("{}")})
	delete(f.Files, SiteVersionFile)
	if _, err := PublishSite(ctx, c, "octo/agent-usage", "main", v, bad); err == nil || f.Commits != 0 {
		t.Fatalf("a file outside site/ must be refused: %v", err)
	}
}

func TestPublishSiteWithTheEmbeddedBuild(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	seedRepo(f)
	v, files, err := Site()
	if err != nil {
		t.Fatal(err)
	}
	if updated, err := PublishSite(ctx, c, "octo/agent-usage", "main", v, files); err != nil || !updated {
		t.Fatalf("embedded build: %v %v", updated, err)
	}
	got, ok := parseSiteVersion([]byte(f.Files[SiteVersionFile]))
	if !ok || got != v {
		t.Fatalf("published version.json %q", f.Files[SiteVersionFile])
	}
	if _, ok := f.Files["site/app.js"]; ok {
		t.Fatal("the first template's app.js must go")
	}
	for _, fl := range files {
		if f.Files[fl.Path] != string(fl.Content) {
			t.Errorf("%s not published byte for byte", fl.Path)
		}
	}
}

// Another machine with a newer build commits it between this machine's read
// of version.json and its commit: the older build must not land on top of it.
func TestPublishSiteNeverOverwritesANewerDashboardThatLandsMeanwhile(t *testing.T) {
	f := ghapitest.New()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	v1, files1 := fakeSite(t0.Add(24*time.Hour), map[string]string{"site/index.html": "<p>v1</p>", "site/assets/a.js": "a()"})
	v2, files2 := fakeSite(t0.Add(48*time.Hour), map[string]string{"site/index.html": "<p>v2</p>", "site/assets/b.js": "b()"})
	var srv *httptest.Server
	raced := false
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !raced && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/") {
			// The newer machine's whole publish, before this request is served.
			raced = true
			other := ghapi.New("ghu_test", "tokenmaxr-test")
			other.API, other.Web = srv.URL, srv.URL
			if updated, err := PublishSite(r.Context(), other, "octo/agent-usage", "main", v2, files2); err != nil || !updated {
				t.Errorf("the newer machine's publish: %v %v", updated, err)
			}
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := ghapi.New("ghu_test", "tokenmaxr-test")
	c.API, c.Web = srv.URL, srv.URL
	_, files0 := fakeSite(t0, map[string]string{"site/index.html": "<p>v0</p>"})
	for _, fl := range files0 {
		f.Files[fl.Path] = string(fl.Content)
	}
	f.Files[MarkerFile] = `{"tokenmaxr":1}`

	updated, err := PublishSite(context.Background(), c, "octo/agent-usage", "main", v1, files1)
	if err != nil || updated || !raced {
		t.Fatalf("the older build: updated %v err %v (raced %v)", updated, err, raced)
	}
	if got, _ := parseSiteVersion([]byte(f.Files[SiteVersionFile])); got != v2 || f.Files["site/index.html"] != "<p>v2</p>" {
		t.Fatalf("downgraded to %q / %q", f.Files["site/index.html"], f.Files[SiteVersionFile])
	}
	if _, ok := f.Files["site/assets/a.js"]; ok || f.Files["site/assets/b.js"] != "b()" || f.Files[MarkerFile] == "" {
		t.Fatalf("files %v", slices.Sorted(maps.Keys(f.Files)))
	}
}
