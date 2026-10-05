package ghapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
)

// A repository renamed on GitHub keeps working under its old name: GitHub
// redirects GETs with 301 and every write (the git data POSTs, the ref
// PATCH) with 307, which the client sends again with the same method, body
// and token. The client marks that it was redirected (Moved).
func TestRenamedRepositoryFollowsGitHubsRedirects(t *testing.T) {
	f := ghapitest.New()
	f.Files["tokenmaxr.json"] = `{"tokenmaxr":1}`
	c := client(t, f)
	ctx := context.Background()
	f.Rename("octo/tokens")

	sha, err := c.Commit(ctx, "octo/agent-usage", "main", "data", []File{{Path: "data/a.json", Content: []byte(`{"n":1}`)}})
	if err != nil || sha == "" {
		t.Fatalf("commit through the old name: %q %v", sha, err)
	}
	if f.Files["data/a.json"] != `{"n":1}` || f.Files["tokenmaxr.json"] != `{"tokenmaxr":1}` || f.Commits != 1 {
		t.Fatalf("the redirected writes lost their bodies: %v (%d commits)", f.Files, f.Commits)
	}
	for _, want := range []string{
		"301 GET /repos/octo/agent-usage/git/ref/heads/main",
		"307 POST /repos/octo/agent-usage/git/trees",
		"307 POST /repos/octo/agent-usage/git/commits",
		"307 PATCH /repos/octo/agent-usage/git/refs/heads/main",
	} {
		if !slices.Contains(f.Redirects, want) {
			t.Errorf("no %q in %q", want, f.Redirects)
		}
	}
	for _, a := range f.AuthSeen {
		if a != "Bearer ghu_test" {
			t.Fatalf("a redirect within the API dropped the token: %q", f.AuthSeen)
		}
	}
	if !c.Moved() || c.Moved() {
		t.Fatal("Moved must report the redirect once")
	}

	// The repository itself says its new name; its id finds it too.
	info, err := c.Repository(ctx, "octo/agent-usage")
	if err != nil || info.FullName != "octo/tokens" || info.ID != ghapitest.RepoID || !c.Moved() {
		t.Fatalf("repository through the old name: %+v %v", info, err)
	}
	f.Pages = true
	if u, _ := c.PagesURL(ctx, info.FullName); u != "https://octo.github.io/tokens/" {
		t.Fatalf("pages url %q", u)
	}
	if _, err := c.Repository(ctx, "octo/tokens"); err != nil || c.Moved() {
		t.Fatalf("a request to the new name is not a move: %v", err)
	}

	// GitHub dropped the redirect (the old name reused, say): the old name
	// is gone, the id still finds it.
	f.NoRedirect = true
	if _, err := c.Repository(ctx, "octo/agent-usage"); StatusOf(err) != http.StatusNotFound {
		t.Fatalf("old name without its redirect: %v", err)
	}
	if info, err := c.RepositoryByID(ctx, ghapitest.RepoID); err != nil || info.FullName != "octo/tokens" {
		t.Fatalf("by id: %+v %v", info, err)
	}
	// Out of the installation's reach: a public repository still reads
	// (by its id too), but the installation does not list it and writes to
	// it are refused.
	f.Uncovered = true
	if info, err := c.RepositoryByID(ctx, ghapitest.RepoID); err != nil || info.FullName != "octo/tokens" {
		t.Fatalf("uncovered by id: %+v %v", info, err)
	}
	if repos, err := c.InstallationRepos(ctx, 7); err != nil || len(repos) != 0 {
		t.Fatalf("uncovered, the installation lists %+v %v", repos, err)
	}
	if err := c.SetHomepage(ctx, "octo/tokens", "x"); StatusOf(err) != http.StatusForbidden {
		t.Fatalf("uncovered write: %v", err)
	}
}

// recorder answers every request with 204 and keeps what it got.
type recorder struct {
	mu   sync.Mutex
	got  []string // "METHOD path auth body"
	code int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.got = append(r.got, req.Method+" "+req.URL.Path+" "+req.Header.Get("Authorization")+" "+string(b))
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// The token goes only to the API's own host: a 307 to anywhere else (here
// another port of the same address, which Go itself would send it to) is
// followed with the body but without the token.
func TestRedirectsKeepTheTokenOnTheAPIHostOnly(t *testing.T) {
	other := &recorder{}
	elsewhere := httptest.NewServer(other)
	t.Cleanup(elsewhere.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/landed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(api.Close)
	c := New("ghu_secret", "tokenmaxr-test")
	c.API, c.Web = api.URL, api.URL
	if err := c.SetHomepage(context.Background(), "octo/agent-usage", "https://octo.github.io/x/"); err != nil {
		t.Fatal(err)
	}
	if len(other.got) != 1 || !strings.HasPrefix(other.got[0], "PATCH /landed  {") || !strings.Contains(other.got[0], `"homepage":"https://octo.github.io/x/"`) {
		t.Fatalf("followed as %q", other.got)
	}
	if strings.Contains(strings.Join(other.got, "\n"), "ghu_secret") {
		t.Fatal("the token left the API host")
	}
}

// A write answered with 301 (GitHub sends 307 for writes) is never turned
// into a GET without its body, which would look like success: the redirect
// is its answer.
func TestWritesAreNotFollowedAsGETs(t *testing.T) {
	target := &recorder{}
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/landed" {
			target.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Location", api.URL+"/landed")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	t.Cleanup(api.Close)
	c := New("ghu_test", "tokenmaxr-test")
	c.API, c.Web = api.URL, api.URL
	ctx := context.Background()
	if err := c.SetHomepage(ctx, "octo/agent-usage", "https://octo.github.io/x/"); StatusOf(err) != http.StatusMovedPermanently {
		t.Fatalf("a 301 on a PATCH: %v", err)
	}
	if len(target.got) != 0 {
		t.Fatalf("the PATCH was sent on as %q", target.got)
	}
	// A GET follows it (and carries the token on the same host).
	if _, err := c.Repository(ctx, "octo/agent-usage"); err != nil {
		t.Fatal(err)
	}
	if len(target.got) != 1 || target.got[0] != "GET /landed Bearer ghu_test " {
		t.Fatalf("GET followed as %q", target.got)
	}
}
