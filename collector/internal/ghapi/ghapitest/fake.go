// Package ghapitest is an in-memory fake of the GitHub endpoints the
// collector uses (device flow, installations, contents, git data, variables,
// Pages), for tests only. It serves one user "octo" (id 42) with one
// installation of the App and one repository, octo/agent-usage.
package ghapitest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Fake is a tiny in-memory GitHub: device flow, contents, git data,
// variables, pages, installations. Enough to exercise every client call.
type Fake struct {
	mu       sync.Mutex
	Polls    int
	PollPlan []string          // device poll outcomes in order: pending, slow_down, ok, denied, expired
	Files    map[string]string // the branch head's content (tests may seed it)
	Head     string
	trees    map[string]map[string]string
	commits  map[string]string // commit -> tree
	parents  map[string]string // commit -> parent
	Vars     map[string]string
	Pages    bool
	MoveOnce bool // simulate another machine moving the branch once
	// Interleave, when set, runs once just before the next branch update
	// lands: it edits a copy of the head's files, which is committed first
	// (as the owner editing on github.com between a read and a commit).
	Interleave func(files map[string]string)
	// Commits counts branch updates; Reads lists the contents paths read
	// ("path@ref" when read at a ref).
	Commits int
	Reads   []string
	// Deletes counts paths removed by commits.
	Deletes int
	// NoInstall: the user has not installed the App yet.
	NoInstall bool
	// Revoked tokens are refused with 401, like a revoked sign-in.
	Revoked map[string]bool
	// Homepage is the repository's website; HomepageSets counts updates.
	// NoAdmin refuses settings edits (403), like an installation without
	// Administration: write.
	Homepage     string
	HomepageSets int
	NoAdmin      bool
	seq          int
	AuthSeen     []string
	// Requests lists every request as asked: "GET /repos/octo/agent-usage".
	Requests []string
	// Repo is the repository's full name now (New: "octo/agent-usage"; its
	// id is RepoID). Rename gives it another, and its old names redirect
	// there (Redirects lists each redirect: "307 POST /repos/..."), unless
	// NoRedirect (GitHub dropped the redirect: 404). Uncovered moves it out
	// of the installation's reach: as a public repository it is still read
	// by any sign-in (by its names and its id), but the installation does
	// not list it and writes to it are refused (403). ByID counts requests
	// by its id.
	Repo       string
	former     []string
	NoRedirect bool
	Uncovered  bool
	Redirects  []string
	ByID       int
}

// RepoID is the fake repository's immutable id.
const RepoID = 1

// Rename renames (or, with another owner, transfers) the repository to
// full ("owner/name"), as on github.com: its old name redirects.
func (f *Fake) Rename(full string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.former = append(f.former, f.full())
	f.Repo = full
}

func (f *Fake) full() string {
	if f.Repo == "" {
		return "octo/agent-usage"
	}
	return f.Repo
}

// pagesURL is the repository's Pages address, as GitHub forms it.
func (f *Fake) pagesURL() string {
	owner, name, _ := strings.Cut(f.full(), "/")
	return "https://" + strings.ToLower(owner) + ".github.io/" + name + "/"
}

func (f *Fake) repoJSON() map[string]any {
	owner, name, _ := strings.Cut(f.full(), "/")
	ownerID := 42
	if owner != "octo" {
		ownerID = 43
	}
	return map[string]any{"id": RepoID, "name": name, "full_name": f.full(), "default_branch": "main", "owner": map[string]any{"login": owner, "id": ownerID}}
}

// underRepo returns what follows the repository path prefix in path (""
// for the repository itself), matching the name in any case, like GitHub.
func underRepo(path, prefix string) (string, bool) {
	if len(path) < len(prefix) || !strings.EqualFold(path[:len(prefix)], prefix) {
		return "", false
	}
	rest := path[len(prefix):]
	if rest != "" && rest[0] != '/' {
		return "", false
	}
	return rest, true
}

func New() *Fake {
	f := &Fake{Files: map[string]string{}, trees: map[string]map[string]string{"t0": {}}, commits: map[string]string{"c0": "t0"}, parents: map[string]string{}, Head: "c0", Vars: map[string]string{}}
	return f
}

// at is the content at ref: Files for the branch or its head (tests seed
// Files directly), an earlier commit's or tree's own files otherwise.
func (f *Fake) at(ref string) map[string]string {
	if c, ok := f.commits[ref]; ok && ref != f.Head {
		return f.trees[c]
	}
	if t, ok := f.trees[ref]; ok && ref != f.commits[f.Head] {
		return t
	}
	return f.Files
}

func (f *Fake) id(p string) string {
	f.seq++
	return p + strings.Repeat("x", 1) + string(rune('a'+f.seq))
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AuthSeen = append(f.AuthSeen, r.Header.Get("Authorization"))
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	body, _ := io.ReadAll(r.Body)
	js := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && f.Revoked[tok] {
		js(401, map[string]string{"message": "Bad credentials"})
		return
	}
	// A repository's old name redirects to its id, as GitHub does after a
	// rename or a transfer: GET and HEAD with 301, the rest with 307.
	path := r.URL.Path
	for _, old := range f.former {
		rest, ok := underRepo(path, "/repos/"+old)
		if !ok {
			continue
		}
		if f.NoRedirect {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		to := fmt.Sprintf("http://%s/repositories/%d%s", r.Host, RepoID, rest)
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
		code := http.StatusTemporaryRedirect
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			code = http.StatusMovedPermanently
		}
		f.Redirects = append(f.Redirects, fmt.Sprintf("%d %s %s", code, r.Method, path))
		w.Header().Set("Location", to)
		js(code, map[string]string{"message": "Moved Permanently", "url": to})
		return
	}
	if rest, ok := underRepo(path, fmt.Sprintf("/repositories/%d", RepoID)); ok {
		f.ByID++
		path = "/repos/" + f.full() + rest
	}
	repo := "/repos/" + f.full()
	if rest, ok := underRepo(path, repo); ok {
		path = repo + rest // GitHub's names are not case-sensitive
		if f.Uncovered && r.Method != http.MethodGet && r.Method != http.MethodHead {
			js(403, map[string]string{"message": "Resource not accessible by integration"})
			return
		}
	}
	switch {
	case path == "/login/device/code":
		form, _ := url.ParseQuery(string(body))
		if form.Get("client_id") != "Iv-test" {
			js(200, map[string]string{"error": "unauthorized_client"})
			return
		}
		js(200, map[string]any{"device_code": "dev-1", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
	case path == "/login/oauth/access_token":
		out := f.PollPlan[min(f.Polls, len(f.PollPlan)-1)]
		f.Polls++
		switch out {
		case "ok":
			js(200, map[string]string{"access_token": "ghu_test", "token_type": "bearer"})
		case "slow_down":
			js(200, map[string]any{"error": "slow_down", "interval": 10})
		case "pending":
			js(200, map[string]string{"error": "authorization_pending"})
		case "denied":
			js(200, map[string]string{"error": "access_denied"})
		case "expired":
			js(200, map[string]string{"error": "expired_token"})
		}
	case path == "/user":
		js(200, map[string]any{"login": "octo", "id": 42})
	case path == "/user/installations" && f.NoInstall:
		js(200, map[string]any{"installations": []any{}})
	case path == "/user/installations":
		js(200, map[string]any{"installations": []map[string]any{{"id": 7, "app_id": 99, "app_slug": "tokenmaxor", "account": map[string]any{"login": "octo", "id": 42}, "repository_selection": "selected"}}})
	case path == "/user/installations/7/repositories" && f.Uncovered:
		js(200, map[string]any{"repositories": []any{}})
	case path == "/user/installations/7/repositories":
		js(200, map[string]any{"repositories": []map[string]any{f.repoJSON()}})
	case strings.HasPrefix(path, repo+"/contents/"):
		p := strings.TrimPrefix(path, repo+"/contents/")
		if ref := r.URL.Query().Get("ref"); ref != "" {
			f.Reads = append(f.Reads, p+"@"+ref)
		} else {
			f.Reads = append(f.Reads, p)
		}
		c, ok := f.at(r.URL.Query().Get("ref"))[p]
		if !ok {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(c)), "encoding": "base64"})
	case path == repo+"/git/ref/heads/main":
		js(200, map[string]any{"object": map[string]string{"sha": f.Head}})
	case strings.HasPrefix(path, repo+"/git/commits/"):
		js(200, map[string]any{"tree": map[string]string{"sha": f.commits[strings.TrimPrefix(path, repo+"/git/commits/")]}})
	case strings.HasPrefix(path, repo+"/git/trees/") && r.Method == http.MethodGet:
		// The files at that tree or ref, recursively.
		files := f.at(strings.TrimPrefix(path, repo+"/git/trees/"))
		var tree []map[string]string
		for _, p := range slices.Sorted(maps.Keys(files)) {
			tree = append(tree, map[string]string{"path": p, "type": "blob", "mode": "100644"})
		}
		js(200, map[string]any{"sha": f.commits[f.Head], "tree": tree, "truncated": false})
	case path == repo+"/git/trees":
		var req struct {
			BaseTree string `json:"base_tree"`
			Tree     []map[string]json.RawMessage
		}
		json.Unmarshal(body, &req)
		// The head's tree holds Files (which tests also seed directly).
		base := f.trees[req.BaseTree]
		if req.BaseTree == f.commits[f.Head] {
			base = f.Files
		}
		next := map[string]string{}
		for k, v := range base {
			next[k] = v
		}
		same := true
		for _, e := range req.Tree {
			var path, content string
			json.Unmarshal(e["path"], &path)
			if sha, ok := e["sha"]; ok && string(sha) == "null" {
				// GitHub cannot delete what the base tree does not have.
				if _, ok := next[path]; !ok {
					js(422, map[string]string{"message": "GitRPC::BadObjectState"})
					return
				}
				delete(next, path)
				f.Deletes++
				same = false
				continue
			}
			json.Unmarshal(e["content"], &content)
			if old, ok := next[path]; !ok || old != content {
				same = false
			}
			next[path] = content
		}
		if same {
			js(201, map[string]string{"sha": req.BaseTree})
			return
		}
		t := f.id("t")
		f.trees[t] = next
		js(201, map[string]string{"sha": t})
	case path == repo+"/git/commits":
		var req struct {
			Tree    string
			Parents []string
		}
		json.Unmarshal(body, &req)
		c := f.id("c")
		f.commits[c] = req.Tree
		if len(req.Parents) > 0 {
			f.parents[c] = req.Parents[0]
		}
		js(201, map[string]string{"sha": c})
	case path == repo+"/git/refs/heads/main":
		var req struct{ SHA string }
		json.Unmarshal(body, &req)
		if edit := f.Interleave; edit != nil {
			f.Interleave = nil
			next := maps.Clone(f.Files)
			edit(next)
			t, c := f.id("t"), f.id("c")
			f.trees[t], f.commits[c], f.parents[c] = next, t, f.Head
			f.Head, f.Files = c, next
			f.Commits++
		}
		ff := false // the new commit descends from the head
		for c := req.SHA; c != "" && !ff; c = f.parents[c] {
			ff = c == f.Head
		}
		if f.MoveOnce || !ff {
			f.MoveOnce = false
			js(422, map[string]string{"message": "Update is not a fast forward"})
			return
		}
		f.Head = req.SHA
		f.Commits++
		f.Files = maps.Clone(f.trees[f.commits[req.SHA]])
		if f.Files == nil {
			f.Files = map[string]string{}
		}
		js(200, map[string]any{})
	case path == repo+"/actions/variables/TOKENMAXR_FLEET_KEY" && r.Method == http.MethodGet:
		v, ok := f.Vars["TOKENMAXR_FLEET_KEY"]
		if !ok {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"name": "TOKENMAXR_FLEET_KEY", "value": v})
	case path == repo+"/actions/variables" && r.Method == http.MethodPost:
		var req struct{ Name, Value string }
		json.Unmarshal(body, &req)
		if _, ok := f.Vars[req.Name]; ok {
			js(409, map[string]string{"message": "Already exists"})
			return
		}
		f.Vars[req.Name] = req.Value
		js(201, map[string]any{})
	case path == repo && r.Method == http.MethodGet:
		var homepage any // GitHub sends null for none
		if f.Homepage != "" {
			homepage = f.Homepage
		}
		out := f.repoJSON()
		out["homepage"] = homepage
		js(200, out)
	case path == repo && r.Method == http.MethodPatch && f.NoAdmin:
		js(403, map[string]string{"message": "Resource not accessible by integration"})
	case path == repo && r.Method == http.MethodPatch:
		var in struct {
			Homepage *string `json:"homepage"`
		}
		json.Unmarshal(body, &in)
		if in.Homepage != nil {
			f.Homepage = *in.Homepage
			f.HomepageSets++
		}
		js(200, map[string]any{"id": RepoID, "full_name": f.full(), "homepage": f.Homepage})
	case path == repo+"/pages" && r.Method == http.MethodPost:
		if f.Pages {
			js(409, map[string]string{"message": "GitHub Pages is already enabled."})
			return
		}
		f.Pages = true
		js(201, map[string]any{})
	case path == repo+"/pages":
		if !f.Pages {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"html_url": f.pagesURL()})
	default:
		js(404, map[string]string{"message": "unexpected " + r.Method + " " + r.URL.Path})
	}
}
