package ghpub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
)

// Linking the usage repository to its dashboard (owner direction 2026-10-05:
// "can collector add a link to the public stats page for logged-in users,
// automatically?"): the repository's website (the About box on its page)
// and a line under the README's title. Each is done once per repository,
// whichever machine of the fleet gets there first, and only where none is,
// so a website or link the owner set, moved, reworded or removed is left
// alone. What is done is recorded in the repository itself, in MarkerFile's
// "linked" (LinkKey), committed with the README line, so a machine that joins
// later, upgrades later or starts afresh reads it there and does not put a
// removed link back. The owner's profile itself is not touched: that is
// theirs to change (a profile README, the website field), and the App is
// granted only the repositories it publishes to.

// ReadmePath is the usage repository's README.
const ReadmePath = "README.md"

// LinkKey is the field of MarkerFile that records the linking: {"readme":
// true, "website": true} once each step is done (true alone means both).
// While "website" is false the website is tried again (LinkResult.Pending).
const LinkKey = "linked"

// DashboardLine is the README line that links the dashboard.
func DashboardLine(pagesURL string) string { return "**[Open the dashboard](" + pagesURL + ")**" }

// WithDashboardLink returns readme with DashboardLine under its title (the
// first line, when it is a "# " heading), else at the top; nil when the
// README already links the dashboard anywhere (with or without the trailing
// slash), or there is no address.
func WithDashboardLink(readme []byte, pagesURL string) []byte {
	bare := strings.TrimSuffix(pagesURL, "/")
	if bare == "" || bytes.Contains(readme, []byte(bare)) {
		return nil
	}
	line := DashboardLine(pagesURL)
	text := string(readme)
	if title, rest, ok := strings.Cut(text, "\n"); ok && strings.HasPrefix(title, "# ") {
		if !strings.HasPrefix(rest, "\n") {
			rest = "\n" + rest
		}
		return []byte(title + "\n\n" + line + "\n" + rest)
	}
	if strings.HasPrefix(text, "# ") { // a README that is only its title
		return []byte(text + "\n\n" + line + "\n")
	}
	return []byte(line + "\n\n" + text)
}

// PagesURLFor is the GitHub Pages address GitHub gives repository full
// ("owner/name") without a custom domain: https://owner.github.io/name/, or
// https://owner.github.io/ for the repository named owner.github.io.
func PagesURLFor(full string) string {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" {
		return ""
	}
	host := strings.ToLower(owner) + ".github.io"
	if strings.EqualFold(name, host) {
		return "https://" + host + "/"
	}
	return "https://" + host + "/" + name + "/"
}

// sameURL compares dashboard addresses, ignoring case and a trailing slash.
func sameURL(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(a), "/"), strings.TrimSuffix(strings.TrimSpace(b), "/"))
}

// MoveDashboardLinks returns readme with every link to one of the former
// dashboard addresses (with or without the trailing slash, and anything
// after it) pointing at pagesURL instead; nil when it links none of them.
// An address is only replaced whole: a longer name that starts with it
// (".../usage-old") is another repository's.
func MoveDashboardLinks(readme []byte, former []string, pagesURL string) []byte {
	to := strings.TrimSuffix(pagesURL, "/")
	text, changed := string(readme), false
	for _, f := range former {
		from := strings.TrimSuffix(strings.TrimSpace(f), "/")
		if from == "" || to == "" || sameURL(from, to) {
			continue
		}
		var out strings.Builder
		rest := text
		for {
			i := strings.Index(rest, from)
			if i < 0 {
				out.WriteString(rest)
				break
			}
			end := i + len(from)
			out.WriteString(rest[:i])
			if continues(rest[end:]) {
				out.WriteString(from)
			} else {
				out.WriteString(to)
				changed = true
			}
			rest = rest[end:]
		}
		text = out.String()
	}
	if !changed {
		return nil
	}
	return []byte(text)
}

// continues reports whether what follows an address (s) continues its last
// name: a name character, or a dot before one (a dot that ends a sentence
// does not).
func continues(s string) bool {
	alnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }
	switch {
	case s == "":
		return false
	case s[0] == '.':
		return len(s) > 1 && (alnum(s[1]) || s[1] == '-' || s[1] == '_')
	}
	return alnum(s[0]) || s[0] == '-' || s[0] == '_'
}

// linkState is what MarkerFile's LinkKey says is done.
type linkState struct {
	Readme  bool `json:"readme"`
	Website bool `json:"website"`
}

// parseLinked reads LinkKey's value: absent, null or false is nothing done,
// true is everything (an owner may write it to keep the collectors out), and
// an object says each step.
func parseLinked(raw json.RawMessage) linkState {
	var all bool
	if json.Unmarshal(raw, &all) == nil {
		return linkState{Readme: all, Website: all}
	}
	var s linkState
	json.Unmarshal(raw, &s)
	return s
}

// markerField is one top-level field of MarkerFile, in the file's order.
type markerField struct {
	key string
	raw json.RawMessage
}

// parseMarker splits MarkerFile (a JSON object) into its fields, in order.
func parseMarker(b []byte) ([]markerField, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%s is not a JSON object", MarkerFile)
	}
	var fields []markerField
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", MarkerFile, err)
		}
		key, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%s: %w", MarkerFile, err)
		}
		fields = append(fields, markerField{key, raw})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, fmt.Errorf("%s is not a JSON object", MarkerFile)
	}
	return fields, nil
}

// withLinked returns MarkerFile with LinkKey set to s and every other field
// as it was, in its order (indented like the collector's other files).
func withLinked(fields []markerField, s linkState) []byte {
	value, _ := json.Marshal(s)
	var buf bytes.Buffer
	buf.WriteByte('{')
	set := false
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		buf.Write(k)
		buf.WriteByte(':')
		if f.key == LinkKey {
			buf.Write(value)
			set = true
		} else {
			buf.Write(f.raw)
		}
	}
	if !set {
		if len(fields) > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`"` + LinkKey + `":`)
		buf.Write(value)
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	json.Indent(&out, buf.Bytes(), "", " ")
	out.WriteByte('\n')
	return out.Bytes()
}

// LinkResult is what LinkDashboard did and what is left.
type LinkResult struct {
	// Website: this call set the repository's website; Readme: it committed
	// the README line.
	Website, Readme bool
	// Pending: the website is not set and this installation may not set it
	// (403 or 404: no Administration: write); try again later. Everything
	// else is done, and the repository records it.
	Pending bool
}

// LinkDashboard points repo at its dashboard pagesURL, once per repository:
// the website when the repository has none, and a README line when the
// README (if there is one) links it nowhere. Steps MarkerFile's LinkKey
// records as done are skipped, whatever the repository looks like now; the
// steps done here are recorded there in one commit (with the README line).
// MarkerFile and README.md are read at one commit and the commit lands only
// on top of it: a branch that moved meanwhile (another machine, or the owner
// editing the README) is read again, never overwritten. An installation that
// cannot edit the repository's settings (403 or 404) leaves the website
// unrecorded (Pending) and still links the README.
//
// former are addresses the dashboard had before the repository was renamed
// or transferred (GitHub Pages does not redirect them): a website that is
// one of them is set to pagesURL, and the README's links to them point at
// pagesURL, whatever the record says (the steps were done, at the old
// address). Anything else the owner set stays. "linked": true, written by
// the owner, keeps the collectors out of this too.
func LinkDashboard(ctx context.Context, c *ghapi.Client, repo, branch, pagesURL string, former ...string) (LinkResult, error) {
	var res LinkResult
	if pagesURL == "" {
		return res, errors.New("no dashboard address")
	}
	var moved []string
	for _, f := range former {
		if f != "" && !sameURL(f, pagesURL) && !slices.ContainsFunc(moved, func(m string) bool { return sameURL(m, f) }) {
			moved = append(moved, f)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		head, err := c.Head(ctx, repo, branch)
		if err != nil {
			return res, err
		}
		marker, err := c.GetFileAt(ctx, repo, MarkerFile, head)
		if err != nil {
			return res, err
		}
		if marker == nil {
			return res, fmt.Errorf("%s has no %s", repo, MarkerFile)
		}
		fields, err := parseMarker(marker)
		if err != nil {
			return res, err
		}
		var was linkState
		relink := moved
		for _, f := range fields {
			if f.key == LinkKey {
				was = parseLinked(f.raw)
				if string(bytes.TrimSpace(f.raw)) == "true" {
					relink = nil // the owner's: the collectors stay out
				}
			}
		}
		now := was
		res.Pending = false
		if !now.Website || len(relink) > 0 {
			info, err := c.Repository(ctx, repo)
			if err != nil {
				return res, err
			}
			home := strings.TrimSpace(info.Homepage)
			set := home == "" && !now.Website || home != "" && slices.ContainsFunc(relink, func(f string) bool { return sameURL(f, home) })
			if home != "" && !set {
				now.Website = true // the owner's, or set by this call's earlier attempt
			}
			if set {
				switch err := c.SetHomepage(ctx, repo, pagesURL); {
				case err == nil:
					now.Website, res.Website = true, true
				case ghapi.StatusOf(err) == http.StatusForbidden, ghapi.StatusOf(err) == http.StatusNotFound:
					res.Pending = true // not this installation's to change, yet
				default:
					return res, err
				}
			}
		}
		var readme []byte // the README to commit (nil: as it is)
		if !now.Readme || len(relink) > 0 {
			current, err := c.GetFileAt(ctx, repo, ReadmePath, head)
			if err != nil {
				return res, err
			}
			text := current
			if next := MoveDashboardLinks(current, relink, pagesURL); next != nil {
				text, readme = next, next
			}
			if !now.Readme {
				if next := WithDashboardLink(text, pagesURL); current != nil && next != nil {
					readme = next
				}
				now.Readme = true
			}
		}
		if now == was && readme == nil {
			return res, nil
		}
		var files []ghapi.File
		msg := "tokenmaxr: record the dashboard link"
		if readme != nil {
			files = append(files, ghapi.File{Path: ReadmePath, Content: readme})
			msg = "README: link the dashboard"
			if was.Readme {
				msg = "README: the dashboard's new address"
			}
		}
		if now != was {
			files = append(files, ghapi.File{Path: MarkerFile, Content: withLinked(fields, now)})
		}
		_, err = c.CommitOn(ctx, repo, branch, head, msg, files)
		if errors.Is(err, ghapi.ErrConflict) {
			continue // the branch moved: read it again
		}
		if err != nil {
			return res, err
		}
		res.Readme = readme != nil
		return res, nil
	}
	return res, ghapi.ErrConflict
}
