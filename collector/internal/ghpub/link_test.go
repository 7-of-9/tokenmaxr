package ghpub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWithDashboardLink(t *testing.T) {
	const url = "https://octo.github.io/agent-usage/"
	line := DashboardLine(url)
	for _, c := range []struct{ name, in, want string }{
		{"under the title", "# usage\n\nThis repository holds…\n", "# usage\n\n" + line + "\n\nThis repository holds…\n"},
		{"title with no blank line", "# usage\nText\n", "# usage\n\n" + line + "\n\nText\n"},
		{"only a title", "# usage", "# usage\n\n" + line + "\n"},
		{"no title", "Some notes\n", line + "\n\nSome notes\n"},
	} {
		if got := string(WithDashboardLink([]byte(c.in), url)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	// Linked already, anywhere and in any words (with or without the slash): left alone.
	for _, in := range []string{"# usage\n\n[stats](" + url + ")\n", "# usage\n\nsee https://octo.github.io/agent-usage\n"} {
		if got := WithDashboardLink([]byte(in), url); got != nil {
			t.Errorf("relinked %q: %q", in, got)
		}
	}
	if WithDashboardLink([]byte("# usage\n"), "") != nil {
		t.Error("linked an empty address")
	}
}

// linked is what the repository's tokenmaxr.json records.
func linked(t *testing.T, marker string) (s linkState, present bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(marker), &m); err != nil {
		t.Fatalf("tokenmaxr.json %q: %v", marker, err)
	}
	raw, ok := m[LinkKey]
	return parseLinked(raw), ok
}

func TestLinkDashboard(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	const url = "https://octo.github.io/agent-usage/"
	f.Files[MarkerFile] = `{"tokenmaxr": 1, "title": "My usage", "background": true}`
	f.Files[ReadmePath] = "# usage\n\nData.\n"
	res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url)
	if err != nil || !res.Website || !res.Readme || res.Pending {
		t.Fatalf("first: %+v, %v", res, err)
	}
	if f.Homepage != url || !strings.Contains(f.Files[ReadmePath], DashboardLine(url)) || f.Commits != 1 {
		t.Fatalf("website %q, %d commits, README %q", f.Homepage, f.Commits, f.Files[ReadmePath])
	}
	// Recorded in the same commit, the owner's settings kept in their order.
	if want := "{\n \"tokenmaxr\": 1,\n \"title\": \"My usage\",\n \"background\": true,\n \"linked\": {\n  \"readme\": true,\n  \"website\": true\n }\n}\n"; f.Files[MarkerFile] != want {
		t.Fatalf("tokenmaxr.json:\n%s\nwant\n%s", f.Files[MarkerFile], want)
	}

	// The owner points the website elsewhere, then clears it, and drops the
	// line. Any machine, one that never linked included (LinkDashboard
	// keeps nothing between calls), leaves both as they are.
	f.Files[ReadmePath] = "# usage\n"
	for _, website := range []string{"https://example.com/mine", ""} {
		f.Homepage = website
		if res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url); err != nil || res != (LinkResult{}) {
			t.Fatalf("again: %+v, %v", res, err)
		}
		if f.Homepage != website || f.Files[ReadmePath] != "# usage\n" || f.Commits != 1 || f.HomepageSets != 1 {
			t.Fatalf("put back: website %q, README %q, %d commits, %d website sets", f.Homepage, f.Files[ReadmePath], f.Commits, f.HomepageSets)
		}
	}

	// A website the owner set is theirs; with no README nothing is created.
	// Both are recorded as done.
	f2, c2 := setup(t)
	f2.Files[MarkerFile] = `{"tokenmaxr":1}`
	f2.Homepage = "https://example.com/mine"
	if res, err := LinkDashboard(ctx, c2, "octo/agent-usage", "main", url); err != nil || res != (LinkResult{}) {
		t.Fatalf("owner's website: %+v, %v", res, err)
	}
	if s, _ := linked(t, f2.Files[MarkerFile]); f2.Homepage != "https://example.com/mine" || f2.HomepageSets != 0 || f2.Commits != 1 || len(f2.Files) != 1 || s != (linkState{true, true}) {
		t.Fatalf("website %q, %d sets, %d commits, files %v", f2.Homepage, f2.HomepageSets, f2.Commits, f2.Files)
	}

	// "linked": true, written by the owner: the collectors stay out.
	f3, c3 := setup(t)
	f3.Files[MarkerFile] = `{"tokenmaxr":1,"linked":true}`
	f3.Files[ReadmePath] = "# usage\n"
	if res, err := LinkDashboard(ctx, c3, "octo/agent-usage", "main", url); err != nil || res != (LinkResult{}) || f3.Homepage != "" || f3.Commits != 0 || f3.Files[ReadmePath] != "# usage\n" {
		t.Fatalf("opted out: %+v, %v, website %q, %d commits", res, err, f3.Homepage, f3.Commits)
	}

	// A tokenmaxr.json that is not an object is not rewritten.
	f4, c4 := setup(t)
	f4.Files[MarkerFile] = `[1]`
	if _, err := LinkDashboard(ctx, c4, "octo/agent-usage", "main", url); err == nil || f4.Commits != 0 || f4.HomepageSets != 0 {
		t.Fatalf("broken tokenmaxr.json: err %v, %d commits", err, f4.Commits)
	}
}

// An installation without Administration: write cannot set the website: the
// README is still linked (once), the website is left unrecorded and tried
// again until the owner grants it.
func TestLinkDashboardWithoutAdministration(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	const url = "https://octo.github.io/agent-usage/"
	f.Files[MarkerFile] = `{"tokenmaxr":1}`
	f.Files[ReadmePath] = "# usage\n"
	f.NoAdmin = true
	res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url)
	if err != nil || res != (LinkResult{Readme: true, Pending: true}) || f.Homepage != "" || !strings.Contains(f.Files[ReadmePath], DashboardLine(url)) {
		t.Fatalf("no admin: %+v, %v, website %q, README %q", res, err, f.Homepage, f.Files[ReadmePath])
	}
	if s, _ := linked(t, f.Files[MarkerFile]); s != (linkState{Readme: true}) {
		t.Fatalf("recorded %+v", s)
	}

	// Still refused: nothing committed. The owner drops the README line.
	f.Files[ReadmePath] = "# usage\n"
	if res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url); err != nil || res != (LinkResult{Pending: true}) || f.Commits != 1 {
		t.Fatalf("still refused: %+v, %v, %d commits", res, err, f.Commits)
	}

	// Granted: the website is set and recorded; the README stays as the owner left it.
	f.NoAdmin = false
	if res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url); err != nil || res != (LinkResult{Website: true}) || f.Homepage != url || f.Files[ReadmePath] != "# usage\n" || f.Commits != 2 {
		t.Fatalf("granted: %+v, %v, website %q, README %q, %d commits", res, err, f.Homepage, f.Files[ReadmePath], f.Commits)
	}
	if s, _ := linked(t, f.Files[MarkerFile]); s != (linkState{true, true}) {
		t.Fatalf("recorded %+v", s)
	}
}

// The README is read at the commit the line is committed on: an edit the
// owner commits in between is read again and kept, never overwritten.
func TestLinkDashboardKeepsAConcurrentEdit(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	const url = "https://octo.github.io/agent-usage/"
	f.Files[MarkerFile] = `{"tokenmaxr":1}`
	f.Files[ReadmePath] = "# usage\n\nOld text.\n"
	f.Interleave = func(files map[string]string) { files[ReadmePath] = "# usage\n\nThe owner's new text.\n" }
	res, err := LinkDashboard(ctx, c, "octo/agent-usage", "main", url)
	if err != nil || !res.Readme {
		t.Fatalf("%+v, %v", res, err)
	}
	if want := "# usage\n\n" + DashboardLine(url) + "\n\nThe owner's new text.\n"; f.Files[ReadmePath] != want {
		t.Fatalf("README %q, want %q", f.Files[ReadmePath], want)
	}
	if f.Commits != 2 { // the owner's and the link
		t.Fatalf("%d commits", f.Commits)
	}

	// The owner links it themselves in between: only the record is committed.
	f2, c2 := setup(t)
	f2.Files[MarkerFile] = `{"tokenmaxr":1}`
	f2.Files[ReadmePath] = "# usage\n"
	f2.Interleave = func(files map[string]string) { files[ReadmePath] = "# usage\n\n[My stats](" + url + ")\n" }
	if res, err := LinkDashboard(ctx, c2, "octo/agent-usage", "main", url); err != nil || res.Readme {
		t.Fatalf("%+v, %v", res, err)
	}
	if s, _ := linked(t, f2.Files[MarkerFile]); f2.Files[ReadmePath] != "# usage\n\n[My stats]("+url+")\n" || s != (linkState{true, true}) {
		t.Fatalf("README %q, recorded %+v", f2.Files[ReadmePath], s)
	}
}
