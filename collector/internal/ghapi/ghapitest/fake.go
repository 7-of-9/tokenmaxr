// Package ghapitest is an in-memory fake of the GitHub endpoints the
// collector uses (device flow, installations, contents, git data, variables,
// Pages), for tests only. It serves one user "octo" (id 42) with one
// installation of the App and one repository, octo/agent-usage.
package ghapitest

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Fake is a tiny in-memory GitHub: device flow, contents, git data,
// variables, pages, installations. Enough to exercise every client call.
type Fake struct {
	mu       sync.Mutex
	Polls    int
	PollPlan []string // device poll outcomes in order: pending, slow_down, ok, denied, expired
	Files    map[string]string
	Head     string
	trees    map[string]map[string]string
	commits  map[string]string // commit -> tree
	Vars     map[string]string
	Pages    bool
	MoveOnce bool // simulate another machine moving the branch once
	// NoInstall: the user has not installed the App yet.
	NoInstall bool
	seq       int
	AuthSeen  []string
}

func New() *Fake {
	f := &Fake{Files: map[string]string{}, trees: map[string]map[string]string{"t0": {}}, commits: map[string]string{"c0": "t0"}, Head: "c0", Vars: map[string]string{}}
	return f
}

func (f *Fake) id(p string) string {
	f.seq++
	return p + strings.Repeat("x", 1) + string(rune('a'+f.seq))
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AuthSeen = append(f.AuthSeen, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	js := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/login/device/code":
		form, _ := url.ParseQuery(string(body))
		if form.Get("client_id") != "Iv-test" {
			js(200, map[string]string{"error": "unauthorized_client"})
			return
		}
		js(200, map[string]any{"device_code": "dev-1", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
	case r.URL.Path == "/login/oauth/access_token":
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
	case r.URL.Path == "/user":
		js(200, map[string]any{"login": "octo", "id": 42})
	case r.URL.Path == "/user/installations" && f.NoInstall:
		js(200, map[string]any{"installations": []any{}})
	case r.URL.Path == "/user/installations":
		js(200, map[string]any{"installations": []map[string]any{{"id": 7, "app_id": 99, "app_slug": "tokenmaxor", "account": map[string]any{"login": "octo", "id": 42}, "repository_selection": "selected"}}})
	case r.URL.Path == "/user/installations/7/repositories":
		js(200, map[string]any{"repositories": []map[string]any{{"id": 1, "name": "agent-usage", "full_name": "octo/agent-usage", "default_branch": "main", "owner": map[string]any{"login": "octo", "id": 42}}}})
	case strings.HasPrefix(r.URL.Path, "/repos/octo/agent-usage/contents/"):
		p := strings.TrimPrefix(r.URL.Path, "/repos/octo/agent-usage/contents/")
		c, ok := f.Files[p]
		if !ok {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(c)), "encoding": "base64"})
	case r.URL.Path == "/repos/octo/agent-usage/git/ref/heads/main":
		js(200, map[string]any{"object": map[string]string{"sha": f.Head}})
	case strings.HasPrefix(r.URL.Path, "/repos/octo/agent-usage/git/commits/"):
		js(200, map[string]any{"tree": map[string]string{"sha": f.commits[strings.TrimPrefix(r.URL.Path, "/repos/octo/agent-usage/git/commits/")]}})
	case r.URL.Path == "/repos/octo/agent-usage/git/trees":
		var req struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct{ Path, Content string }
		}
		json.Unmarshal(body, &req)
		next := map[string]string{}
		for k, v := range f.trees[req.BaseTree] {
			next[k] = v
		}
		same := true
		for _, e := range req.Tree {
			if next[e.Path] != e.Content {
				same = false
			}
			next[e.Path] = e.Content
		}
		if same {
			js(201, map[string]string{"sha": req.BaseTree})
			return
		}
		t := f.id("t")
		f.trees[t] = next
		js(201, map[string]string{"sha": t})
	case r.URL.Path == "/repos/octo/agent-usage/git/commits":
		var req struct {
			Tree    string
			Parents []string
		}
		json.Unmarshal(body, &req)
		c := f.id("c")
		f.commits[c] = req.Tree
		js(201, map[string]string{"sha": c})
	case r.URL.Path == "/repos/octo/agent-usage/git/refs/heads/main":
		var req struct{ SHA string }
		json.Unmarshal(body, &req)
		if f.MoveOnce {
			f.MoveOnce = false
			js(422, map[string]string{"message": "Update is not a fast forward"})
			return
		}
		f.Head = req.SHA
		for k, v := range f.trees[f.commits[req.SHA]] {
			f.Files[k] = v
		}
		js(200, map[string]any{})
	case r.URL.Path == "/repos/octo/agent-usage/actions/variables/TOKENMAXR_FLEET_KEY" && r.Method == http.MethodGet:
		v, ok := f.Vars["TOKENMAXR_FLEET_KEY"]
		if !ok {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"name": "TOKENMAXR_FLEET_KEY", "value": v})
	case r.URL.Path == "/repos/octo/agent-usage/actions/variables" && r.Method == http.MethodPost:
		var req struct{ Name, Value string }
		json.Unmarshal(body, &req)
		if _, ok := f.Vars[req.Name]; ok {
			js(409, map[string]string{"message": "Already exists"})
			return
		}
		f.Vars[req.Name] = req.Value
		js(201, map[string]any{})
	case r.URL.Path == "/repos/octo/agent-usage/pages" && r.Method == http.MethodPost:
		if f.Pages {
			js(409, map[string]string{"message": "GitHub Pages is already enabled."})
			return
		}
		f.Pages = true
		js(201, map[string]any{})
	case r.URL.Path == "/repos/octo/agent-usage/pages":
		if !f.Pages {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]string{"html_url": "https://octo.github.io/agent-usage/"})
	default:
		js(404, map[string]string{"message": "unexpected " + r.Method + " " + r.URL.Path})
	}
}
