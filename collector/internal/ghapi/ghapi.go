// Package ghapi is the small slice of GitHub the collector's GitHub publisher
// needs: the OAuth device flow for a GitHub App (only the public client id is
// used; a desktop app cannot keep a secret), and the REST calls to find the
// user's tokenmaxr repo, keep the fleet key in a repository Actions variable
// (readable only by collaborators), switch Pages on and publish files in one
// commit. No third-party dependencies; every base URL is overridable for tests.
package ghapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to GitHub as one user (user-to-server token).
type Client struct {
	// API is the REST base (https://api.github.com); Web is the OAuth host
	// (https://github.com). Tests point both at an httptest server.
	API, Web string
	Token    string
	HTTP     *http.Client
	// UserAgent is required by GitHub.
	UserAgent string
}

// New returns a client for github.com.
func New(token, userAgent string) *Client {
	return &Client{API: "https://api.github.com", Web: "https://github.com", Token: token,
		HTTP: &http.Client{Timeout: 30 * time.Second}, UserAgent: userAgent}
}

// Error is a GitHub API failure. Message is GitHub's short message, which
// never contains credentials.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

// StatusOf returns the HTTP status of a GitHub error (0 for other errors).
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func (c *Client) do(ctx context.Context, method, base, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" && base == c.API {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("GitHub is unreachable")
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var m struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &m)
		msg := m.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &Error{Status: res.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// --- device flow ---

// DeviceCode is GitHub's device authorization response.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// form posts an OAuth form and decodes the JSON reply (the OAuth endpoints
// answer 200 with an "error" field while a device code is pending).
func (c *Client) form(ctx context.Context, path string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Web+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("GitHub is unreachable")
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return &Error{Status: res.StatusCode, Message: "sign-in request rejected"}
	}
	return json.Unmarshal(data, out)
}

// StartDevice asks GitHub for a device code for the App with clientID.
func (c *Client) StartDevice(ctx context.Context, clientID string) (DeviceCode, error) {
	var d DeviceCode
	err := c.form(ctx, "/login/device/code", url.Values{"client_id": {clientID}}, &d)
	if err == nil && (d.DeviceCode == "" || d.UserCode == "") {
		err = errors.New("GitHub returned no device code (is Device Flow enabled for the App?)")
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return d, err
}

// Device-flow outcomes.
var (
	ErrDenied  = errors.New("sign-in was cancelled on GitHub")
	ErrExpired = errors.New("the sign-in code expired; start again")
)

// PollDevice waits until the user approves (returning the access token), the
// code expires, or ctx ends. It follows GitHub's interval and slow_down.
func (c *Client) PollDevice(ctx context.Context, clientID string, d DeviceCode, sleep func(context.Context, time.Duration) error) (string, error) {
	if sleep == nil {
		sleep = sleepCtx
	}
	interval := time.Duration(d.Interval) * time.Second
	for {
		if err := sleep(ctx, interval); err != nil {
			return "", err
		}
		var r struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		if err := c.form(ctx, "/login/oauth/access_token", url.Values{
			"client_id": {clientID}, "device_code": {d.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &r); err != nil {
			return "", err
		}
		switch r.Error {
		case "":
			if r.AccessToken == "" {
				return "", errors.New("GitHub returned no token")
			}
			return r.AccessToken, nil
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
			if r.Interval > 0 {
				interval = time.Duration(r.Interval) * time.Second
			}
		case "access_denied":
			return "", ErrDenied
		case "expired_token", "token_expired":
			return "", ErrExpired
		default:
			return "", fmt.Errorf("sign-in failed (%s)", r.Error)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- users, installations, repositories ---

// User is the signed-in GitHub user. ID is immutable (logins can change).
type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

func (c *Client) User(ctx context.Context) (User, error) {
	var u User
	return u, c.do(ctx, http.MethodGet, c.API, "/user", nil, &u)
}

// Installation is one installation of the App visible to the user.
type Installation struct {
	ID      int64  `json:"id"`
	AppID   int64  `json:"app_id"`
	AppSlug string `json:"app_slug"`
	Account struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"account"`
	RepositorySelection string `json:"repository_selection"`
}

// Installations lists the App's installations the user can access.
func (c *Client) Installations(ctx context.Context) ([]Installation, error) {
	var r struct {
		Installations []Installation `json:"installations"`
	}
	return r.Installations, c.do(ctx, http.MethodGet, c.API, "/user/installations?per_page=100", nil, &r)
}

// Repo is a repository visible through an installation.
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	Owner         struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"owner"`
}

// InstallationRepos lists the repositories an installation grants.
func (c *Client) InstallationRepos(ctx context.Context, installationID int64) ([]Repo, error) {
	var out []Repo
	for page := 1; page <= 10; page++ {
		var r struct {
			Repositories []Repo `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, c.API, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", installationID, page), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Repositories...)
		if len(r.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// --- contents ---

// GetFile returns a file's bytes (nil, nil when it does not exist).
func (c *Client) GetFile(ctx context.Context, repo, path string) ([]byte, error) {
	var r struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/contents/"+escapePath(path), nil, &r)
	if StatusOf(err) == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.Encoding != "base64" {
		return nil, errors.New("unexpected file encoding")
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(r.Content, "\n", ""))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// --- one commit with several files (Git data API) ---

// File is one path to write in a commit.
type File struct {
	Path    string
	Content []byte
}

// ErrConflict: the branch moved while committing (another machine published).
var ErrConflict = errors.New("branch moved during commit")

// Commit writes files on branch in a single commit on top of its head. It
// returns ErrConflict when another writer moved the branch first; callers
// retry, which is safe because each machine writes only its own paths.
func (c *Client) Commit(ctx context.Context, repo, branch, message string, files []File) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref); err != nil {
		return "", err
	}
	var head struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/git/commits/"+ref.Object.SHA, nil, &head); err != nil {
		return "", err
	}
	type entry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	entries := make([]entry, 0, len(files))
	for _, f := range files {
		entries = append(entries, entry{Path: f.Path, Mode: "100644", Type: "blob", Content: string(f.Content)})
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, c.API, "/repos/"+repo+"/git/trees", map[string]any{"base_tree": head.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if tree.SHA == head.Tree.SHA {
		return ref.Object.SHA, nil // nothing changed
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodPost, c.API, "/repos/"+repo+"/git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": []string{ref.Object.SHA}}, &commit); err != nil {
		return "", err
	}
	err := c.do(ctx, http.MethodPatch, c.API, "/repos/"+repo+"/git/refs/heads/"+url.PathEscape(branch), map[string]any{"sha": commit.SHA, "force": false}, nil)
	if s := StatusOf(err); s == http.StatusUnprocessableEntity || s == http.StatusConflict {
		return "", ErrConflict
	}
	return commit.SHA, err
}

// --- Actions variables (the fleet key) ---

// GetVariable returns a repository Actions variable ("" and false if absent).
func (c *Client) GetVariable(ctx context.Context, repo, name string) (string, bool, error) {
	var v struct {
		Value string `json:"value"`
	}
	err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/actions/variables/"+url.PathEscape(name), nil, &v)
	if StatusOf(err) == http.StatusNotFound {
		return "", false, nil
	}
	return v.Value, err == nil, err
}

// CreateVariable creates a repository Actions variable; ErrConflict if it exists.
func (c *Client) CreateVariable(ctx context.Context, repo, name, value string) error {
	err := c.do(ctx, http.MethodPost, c.API, "/repos/"+repo+"/actions/variables", map[string]string{"name": name, "value": value}, nil)
	if StatusOf(err) == http.StatusConflict {
		return ErrConflict
	}
	return err
}

// --- Pages ---

// EnablePages switches GitHub Pages on, built by Actions. Already enabled is fine.
func (c *Client) EnablePages(ctx context.Context, repo string) error {
	err := c.do(ctx, http.MethodPost, c.API, "/repos/"+repo+"/pages", map[string]string{"build_type": "workflow"}, nil)
	if StatusOf(err) == http.StatusConflict {
		return nil
	}
	return err
}

// PagesURL returns the Pages site URL ("" if Pages is not on).
func (c *Client) PagesURL(ctx context.Context, repo string) (string, error) {
	var p struct {
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, http.MethodGet, c.API, "/repos/"+repo+"/pages", nil, &p)
	if StatusOf(err) == http.StatusNotFound {
		return "", nil
	}
	return p.HTMLURL, err
}
