package ghpub

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
)

// site is the built dashboard, a copy of the tokenmaxr repository's
// pages/site made by `npm run build:pages` (scripts/sync-pages-site.mjs);
// "all:" keeps files whose names start with "_" or ".".
//
//go:embed all:site
var site embed.FS

// SiteDir is the dashboard's folder in a usage repository; SiteVersionFile
// identifies the dashboard there.
const (
	SiteDir         = "site"
	SiteVersionFile = SiteDir + "/version.json"
)

// SiteVersion is site/version.json: when the dashboard was built and the
// hash of its files (SiteHash). Collectors order dashboards by BuiltAt, so
// one running an older release never replaces a newer dashboard.
type SiteVersion struct {
	BuiltAt time.Time `json:"builtAt"`
	Hash    string    `json:"hash"`
}

// parseSiteVersion reads a version.json; false when it is not one this
// collector understands (BuiltAt and a sha256 hash are required).
func parseSiteVersion(b []byte) (SiteVersion, bool) {
	var v SiteVersion
	if json.Unmarshal(b, &v) != nil || v.BuiltAt.IsZero() || len(v.Hash) != 64 {
		return SiteVersion{}, false
	}
	return v, true
}

// SiteHash is the hash version.json records for files (repository paths
// under site/; version.json itself is left out): sha256 of one
// "<sha256 hex of the file>  <path below site/>\n" line per file in path
// order. scripts/sync-pages-site.mjs computes the same.
func SiteHash(files []ghapi.File) string {
	var lines []string
	for _, f := range files {
		if f.Path == SiteVersionFile || f.Delete {
			continue
		}
		s := sha256.Sum256(f.Content)
		lines = append(lines, hex.EncodeToString(s[:])+"  "+strings.TrimPrefix(f.Path, SiteDir+"/")+"\n")
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	s := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(s[:])
}

// Site returns the embedded dashboard: its version and every file at its
// repository path (site/...), in path order, version.json included.
func Site() (SiteVersion, []ghapi.File, error) {
	return siteFiles(site)
}

func siteFiles(fsys fs.FS) (SiteVersion, []ghapi.File, error) {
	var files []ghapi.File
	err := fs.WalkDir(fsys, SiteDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		files = append(files, ghapi.File{Path: p, Content: b})
		return err
	})
	if err != nil {
		return SiteVersion{}, nil, err
	}
	i := slices.IndexFunc(files, func(f ghapi.File) bool { return f.Path == SiteVersionFile })
	if i < 0 {
		return SiteVersion{}, nil, errors.New("the embedded dashboard has no version.json")
	}
	v, ok := parseSiteVersion(files[i].Content)
	if !ok || v.Hash != SiteHash(files) {
		return SiteVersion{}, nil, errors.New("the embedded dashboard's version.json does not match its files")
	}
	return v, files, nil
}

// PublishSite brings the repository's dashboard up to v (files from Site):
// when the repository's site/version.json is missing or older, one commit
// writes every site/ file of files and deletes the site/ files it no longer
// has (e.g. an earlier build's assets). It touches nothing outside site/, and
// a version.json it cannot read (a newer format) is left alone. The version
// and the file list are read at one commit and the update lands only on top
// of it, so a newer dashboard another machine commits meanwhile is never
// overwritten: the branch moved, and the retry finds that newer version.
// updated reports a commit.
func PublishSite(ctx context.Context, c *ghapi.Client, repo, branch string, v SiteVersion, files []ghapi.File) (updated bool, err error) {
	have := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, SiteDir+"/") {
			return false, fmt.Errorf("dashboard file %q is outside %s/", f.Path, SiteDir)
		}
		have[f.Path] = true
	}
	for attempt := 0; attempt < 3; attempt++ {
		head, err := c.Head(ctx, repo, branch)
		if err != nil {
			return false, err
		}
		b, err := c.GetFileAt(ctx, repo, SiteVersionFile, head)
		if err != nil {
			return false, err
		}
		if b != nil {
			cur, ok := parseSiteVersion(b)
			if !ok || !v.BuiltAt.After(cur.BuiltAt) {
				return false, nil // the same, a newer or an unknown dashboard
			}
		}
		tree, err := c.TreeOf(ctx, repo, head)
		if err != nil {
			return false, err
		}
		paths, err := c.TreePaths(ctx, repo, tree)
		if err != nil {
			return false, err
		}
		commit := slices.Clone(files)
		for _, p := range paths {
			if strings.HasPrefix(p, SiteDir+"/") && !have[p] {
				commit = append(commit, ghapi.File{Path: p, Delete: true})
			}
		}
		_, err = c.CommitOn(ctx, repo, branch, head, "tokenmaxr: dashboard built "+v.BuiltAt.UTC().Format(time.RFC3339), commit)
		if errors.Is(err, ghapi.ErrConflict) {
			continue // another machine published: maybe this or a newer dashboard
		}
		return err == nil, err
	}
	return false, ghapi.ErrConflict
}
