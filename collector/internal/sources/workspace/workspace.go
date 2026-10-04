// Package workspace holds the workspace rules shared with the API
// (api/src/lib/workspaces.js; SPEC "Workspaces"): canonical keys for the
// prompt archive, and the git repository root that new prompt records use.
//
// The canonical rules, in order:
//
//  1. Normalize: Windows paths are case-folded with "\" separators; WSL paths
//     (/mnt/<drive>/…, \\wsl$\<distro>\…, vscode-remote://wsl+<distro>/…,
//     \home\… written with backslashes) become the path the other side sees;
//     POSIX paths keep their case. A tool's project data folder
//     (…/.cursor/projects/<slug>, …/.claude/projects/<slug>) is its slug.
//  2. Slugs (Claude project directory names such as C--Users-me-src-app, WSL
//     ones such as home-me-src-app) are matched against the slug form of every
//     known real path and its ancestors, else decoded best effort. A 64-hex
//     Gemini project hash is matched against sha256 of a known real path.
//  3. Fold: a workspace moves to the nearest ancestor that is itself a
//     workspace in the set and is not a generic container (a drive or share
//     root, a home or the folder of homes, or a folder named src, repos, tmp,
//     …), repeated until none applies.
//
// testdata/vectors.json holds cases that both implementations must pass.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// GenericNames are folders that hold many unrelated projects: never a fold target.
var GenericNames = map[string]bool{
	"src": true, "source": true, "sources": true, "repos": true, "repo": true, "code": true,
	"projects": true, "dev": true, "git": true, "github": true, "work": true, "workspace": true,
	"workspaces": true, "documents": true, "desktop": true, "downloads": true, "tmp": true,
	"temp": true, "sandbox": true,
}

var (
	hex64         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	driveRe       = regexp.MustCompile(`^[A-Za-z]:(?:[\\/]|$)`)
	uncRe         = regexp.MustCompile(`^[\\/]{2}[^\\/]`)
	wslUNCRe      = regexp.MustCompile(`(?i)^[\\/]{2}(?:wsl\$|wsl\.localhost)[\\/][^\\/]+(.*)$`)
	mntRe         = regexp.MustCompile(`^/mnt/([A-Za-z])(/.*)?$`)
	wslRemoteRe   = regexp.MustCompile(`(?i)^vscode-remote://wsl(?:\+|%2B)[^/]+(/.*)?$`)
	toolProjectRe = regexp.MustCompile(`(?i)[\\/]\.(?:cursor|claude)[\\/]projects[\\/]([^\\/]+)[\\/]*$`)
	slugChars     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	nonAlnum      = regexp.MustCompile(`[^a-z0-9]+`)
	mntSlug       = regexp.MustCompile(`^mnt-([a-z])-`)
	winSlug       = regexp.MustCompile(`^([a-z])-+users-([^-]+)(?:-(.+))?$`)
	homeSlug      = regexp.MustCompile(`^home-([^-]+)(?:-(.+))?$`)
	macSlug       = regexp.MustCompile(`^users-([^-]+)(?:-(.+))?$`)
	manyBack      = regexp.MustCompile(`\\{2,}`)
	manySlash     = regexp.MustCompile(`/{2,}`)
	driveOnly     = regexp.MustCompile(`^[A-Za-z]:$`)
	driveRoot     = regexp.MustCompile(`^[a-z]:\\$`)
	winHomes      = regexp.MustCompile(`^[a-z]:\\users(\\[^\\]+)?$`)
	posixHomes    = regexp.MustCompile(`^/(home|users)(/[^/]+)?$`)
	posixMnt      = regexp.MustCompile(`^/mnt(/[a-z])?$`)
)

func isWindowsKey(k string) bool { return driveRe.MatchString(k) || uncRe.MatchString(k) }

// IsRealPath reports whether a normalised key is an absolute path.
func IsRealPath(k string) bool { return k != "" && (isWindowsKey(k) || strings.HasPrefix(k, "/")) }

// IsSlug reports whether s looks like a path flattened into one name.
func IsSlug(s string) bool {
	return !hex64.MatchString(s) && slugChars.MatchString(s) && strings.Contains(s, "-")
}

// Normalize is rule 1 for one string. Slugs are lowercased; hashes and
// anything unrecognised come back trimmed.
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if len(s) > 7 && strings.EqualFold(s[:7], "file://") {
		u, err := url.Parse(s)
		if err != nil {
			return s
		}
		s = u.Path
		if len(s) >= 3 && s[0] == '/' && s[2] == ':' {
			s = s[1:]
		}
	}
	if m := wslRemoteRe.FindStringSubmatch(s); m != nil {
		s = m[1]
		if s == "" {
			s = "/"
		}
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
	}
	// A tool's per-project data folder is named by the project's slug.
	if m := toolProjectRe.FindStringSubmatch(s); m != nil {
		return Normalize(m[1])
	}
	if m := wslUNCRe.FindStringSubmatch(s); m != nil {
		s = strings.ReplaceAll(m[1], `\`, "/")
		if s == "" {
			s = "/"
		}
	}
	// \home\me\x: a Linux path written with Windows separators.
	if len(s) > 1 && s[0] == '\\' && s[1] != '\\' {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	if m := mntRe.FindStringSubmatch(s); m != nil {
		rest := m[2]
		if rest == "" {
			rest = `\`
		}
		s = m[1] + ":" + rest
	}
	if driveRe.MatchString(s) || uncRe.MatchString(s) {
		unc := uncRe.MatchString(s)
		p := manyBack.ReplaceAllString(strings.ReplaceAll(s, "/", `\`), `\`)
		if unc {
			p = `\` + p
		}
		if len(p) > 3 {
			p = strings.TrimRight(p, `\`)
		}
		if driveOnly.MatchString(p) {
			p += `\`
		}
		return strings.ToLower(p)
	}
	if strings.HasPrefix(s, "/") {
		p := manySlash.ReplaceAllString(s, "/")
		if len(p) > 1 {
			p = strings.TrimRight(p, "/")
			if p == "" {
				p = "/"
			}
		}
		return p
	}
	if IsSlug(s) {
		return strings.ToLower(s)
	}
	return s
}

// SlugForm is the comparable form of a path or slug.
func SlugForm(s string) string {
	f := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
	return mntSlug.ReplaceAllString(f, "$1-")
}

// DecodeSlug is the best-effort decode of a slug with no known path behind it.
func DecodeSlug(slug string) string {
	s := mntSlug.ReplaceAllString(strings.TrimLeft(strings.ToLower(slug), "-"), "$1-")
	rest := func(tail, sep string) string {
		if strings.HasPrefix(tail, "src-") {
			return "src" + sep + tail[4:]
		}
		return tail
	}
	if m := winSlug.FindStringSubmatch(s); m != nil {
		out := m[1] + `:\users\` + m[2]
		if m[3] != "" {
			out += `\` + rest(m[3], `\`)
		}
		return out
	}
	if m := homeSlug.FindStringSubmatch(s); m != nil {
		out := "/home/" + m[1]
		if m[2] != "" {
			out += "/" + rest(m[2], "/")
		}
		return out
	}
	if m := macSlug.FindStringSubmatch(s); m != nil {
		out := "/Users/" + m[1]
		if m[2] != "" {
			out += "/" + rest(m[2], "/")
		}
		return out
	}
	return s
}

// Parent is a key's parent key, or "" at a root.
func Parent(k string) string {
	if isWindowsKey(k) {
		if driveRoot.MatchString(k) {
			return ""
		}
		i := strings.LastIndexByte(k, '\\')
		if i < 0 {
			return ""
		}
		p := k[:i]
		if driveOnly.MatchString(p) {
			return p + `\`
		}
		if strings.HasPrefix(p, `\\`) && len(strings.Split(p[2:], `\`)) < 2 {
			return ""
		}
		return p
	}
	if strings.HasPrefix(k, "/") {
		if k == "/" {
			return ""
		}
		i := strings.LastIndexByte(k, '/')
		if i == 0 {
			return "/"
		}
		return k[:i]
	}
	return ""
}

// BaseName is the last path element of a key.
func BaseName(k string) string {
	parts := strings.FieldsFunc(k, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return k
	}
	return parts[len(parts)-1]
}

// IsGeneric reports whether a key is a folder of unrelated projects.
func IsGeneric(k string) bool {
	if k == "" {
		return true
	}
	if GenericNames[strings.ToLower(BaseName(k))] {
		return true
	}
	if isWindowsKey(k) {
		if driveRoot.MatchString(k) || winHomes.MatchString(k) {
			return true
		}
		return strings.HasPrefix(k, `\\`) && len(strings.Split(k[2:], `\`)) <= 2
	}
	if strings.HasPrefix(k, "/") {
		l := strings.ToLower(k)
		return l == "/" || posixHomes.MatchString(l) || l == "/root" || posixMnt.MatchString(l)
	}
	return false
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Canonicalize applies rules 1–3 to every raw workspace (with its prompt
// count) and returns raw -> root key ("" for an empty workspace).
func Canonicalize(counts map[string]int) map[string]string {
	raws := make([]string, 0, len(counts))
	for r := range counts {
		raws = append(raws, r)
	}
	sort.Strings(raws)
	norm := map[string]string{}
	weight := map[string]int{}
	for _, r := range raws {
		k := Normalize(r)
		norm[r] = k
		weight[k] += counts[r]
	}
	bySlug := map[string]string{}
	byHash := map[string]string{}
	offer := func(m map[string]string, form, key string) {
		prev, ok := m[form]
		if !ok || weight[key] > weight[prev] || (weight[key] == weight[prev] && key < prev) {
			m[form] = key
		}
	}
	for _, r := range raws {
		k := norm[r]
		if !IsRealPath(k) {
			continue
		}
		for a := k; a != ""; a = Parent(a) {
			offer(bySlug, SlugForm(a), a)
		}
		offer(byHash, sha256Hex(strings.TrimSpace(r)), k)
	}
	resolve := func(k string) string {
		switch {
		case k == "" || IsRealPath(k):
			return k
		case hex64.MatchString(k):
			if v, ok := byHash[k]; ok {
				return v
			}
			return k
		case IsSlug(k):
			if v, ok := bySlug[SlugForm(k)]; ok {
				return v
			}
			return DecodeSlug(k)
		}
		return k
	}
	resolved := map[string]string{}
	set := map[string]bool{}
	for _, r := range raws {
		v := resolve(norm[r])
		resolved[r] = v
		if v != "" {
			set[v] = true
		}
	}
	fold := func(k string) string {
		for guard := 0; guard < 64; guard++ {
			next := ""
			for a := Parent(k); a != ""; a = Parent(a) {
				if set[a] && !IsGeneric(a) {
					next = a
					break
				}
			}
			if next == "" {
				return k
			}
			k = next
		}
		return k
	}
	out := make(map[string]string, len(raws))
	for _, r := range raws {
		k := resolved[r]
		if IsRealPath(k) {
			k = fold(k)
		}
		out[r] = k
	}
	return out
}
