package ghpub

import (
	"context"
	"testing"
)

func TestPagesURLFor(t *testing.T) {
	for in, want := range map[string]string{
		"7-of-9/tokens":          "https://7-of-9.github.io/tokens/",
		"Octo/Agent-Usage":       "https://octo.github.io/Agent-Usage/",
		"octo/octo.github.io":    "https://octo.github.io/",
		"octo":                   "",
		"/tokens":                "",
		"octo/":                  "",
		"7-of-9/tokenmaxr-usage": "https://7-of-9.github.io/tokenmaxr-usage/",
	} {
		if got := PagesURLFor(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestMoveDashboardLinks(t *testing.T) {
	const old, url = "https://octo.github.io/agent-usage/", "https://octo.github.io/tokens/"
	former := []string{old}
	for _, c := range []struct{ name, in, want string }{
		{"the collector's line", "# usage\n\n" + DashboardLine(old) + "\n", "# usage\n\n" + DashboardLine(url) + "\n"},
		{"without the slash, twice", "see https://octo.github.io/agent-usage and https://octo.github.io/agent-usage.\n", "see https://octo.github.io/tokens and https://octo.github.io/tokens.\n"},
		{"a page under it", "[Agents](https://octo.github.io/agent-usage/#/agents)", "[Agents](https://octo.github.io/tokens/#/agents)"},
	} {
		if got := string(MoveDashboardLinks([]byte(c.in), former, url)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	for _, in := range []string{
		"# usage\n\n" + DashboardLine(url) + "\n",        // linked to the new address already
		"[old](https://octo.github.io/agent-usage-old/)", // another repository's
		"[v2](https://octo.github.io/agent-usage.v2/)",   // another repository's
		"see https://example.com/agent-usage/",           // not the dashboard
		"",
	} {
		if got := MoveDashboardLinks([]byte(in), former, url); got != nil {
			t.Errorf("moved %q: %q", in, got)
		}
	}
	if MoveDashboardLinks([]byte(DashboardLine(old)), []string{"", url}, url) != nil {
		t.Error("an empty or the same address moved a link")
	}
}

// After a rename (GitHub Pages does not redirect the old address), the
// website and the README links that pointed at the old dashboard address
// point at the new one, once; anything the owner set otherwise stays, and so
// does a repository whose owner keeps the collectors out.
func TestLinkDashboardFollowsARename(t *testing.T) {
	const old, url = "https://octo.github.io/agent-usage/", "https://octo.github.io/tokens/"
	ctx := context.Background()
	f, c := setup(t)
	f.Files[MarkerFile] = `{"tokenmaxr":1,"linked":{"readme":true,"website":true}}`
	f.Files[ReadmePath] = "# usage\n\n" + DashboardLine(old) + "\n\nSee also [the agents](" + old + "#/agents).\n"
	f.Homepage = old
	f.Rename("octo/tokens")
	marker := f.Files[MarkerFile]

	res, err := LinkDashboard(ctx, c, "octo/tokens", "main", url, old, PagesURLFor("octo/agent-usage"), "")
	if err != nil || !res.Website || !res.Readme || res.Pending {
		t.Fatalf("relink: %+v %v", res, err)
	}
	if want := "# usage\n\n" + DashboardLine(url) + "\n\nSee also [the agents](" + url + "#/agents).\n"; f.Files[ReadmePath] != want || f.Homepage != url || f.Commits != 1 {
		t.Fatalf("website %q, README %q, %d commits", f.Homepage, f.Files[ReadmePath], f.Commits)
	}
	if f.Files[MarkerFile] != marker {
		t.Fatalf("the record changed: %s", f.Files[MarkerFile])
	}
	// Every other machine that follows the rename finds nothing to move.
	if res, err := LinkDashboard(ctx, c, "octo/tokens", "main", url, old); err != nil || res != (LinkResult{}) || f.Commits != 1 || f.HomepageSets != 1 {
		t.Fatalf("again: %+v %v (%d commits, %d website sets)", res, err, f.Commits, f.HomepageSets)
	}

	// The owner's own website and README wording stay.
	f2, c2 := setup(t)
	f2.Files[MarkerFile] = `{"tokenmaxr":1,"linked":{"readme":true,"website":true}}`
	f2.Files[ReadmePath] = "# usage\n\n[my stats](https://example.com/stats)\n"
	f2.Homepage = "https://example.com/mine"
	if res, err := LinkDashboard(ctx, c2, "octo/agent-usage", "main", url, old); err != nil || res != (LinkResult{}) || f2.Commits != 0 || f2.HomepageSets != 0 {
		t.Fatalf("owner's links: %+v %v", res, err)
	}

	// Not linked yet when renamed: linked at the new address (the README's
	// old line moved rather than a second one added).
	f3, c3 := setup(t)
	f3.Files[MarkerFile] = `{"tokenmaxr":1}`
	f3.Files[ReadmePath] = "# usage\n\n" + DashboardLine(old) + "\n"
	if res, err := LinkDashboard(ctx, c3, "octo/agent-usage", "main", url, old); err != nil || !res.Website || !res.Readme {
		t.Fatalf("first link after a rename: %+v %v", res, err)
	}
	if s, _ := linked(t, f3.Files[MarkerFile]); f3.Files[ReadmePath] != "# usage\n\n"+DashboardLine(url)+"\n" || f3.Homepage != url || s != (linkState{true, true}) {
		t.Fatalf("README %q, website %q, marker %s", f3.Files[ReadmePath], f3.Homepage, f3.Files[MarkerFile])
	}

	// "linked": true: the owner keeps the collectors out of moving too.
	f4, c4 := setup(t)
	f4.Files[MarkerFile] = `{"tokenmaxr":1,"linked":true}`
	f4.Files[ReadmePath] = DashboardLine(old)
	f4.Homepage = old
	if res, err := LinkDashboard(ctx, c4, "octo/agent-usage", "main", url, old); err != nil || res != (LinkResult{}) || f4.Commits != 0 || f4.Homepage != old {
		t.Fatalf("opted out: %+v %v", res, err)
	}
}
