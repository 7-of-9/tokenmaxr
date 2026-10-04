// Package buildinfo is the release identity baked into a build: the server a
// new install reports to by default (none: GitHub or local only), where
// signed self-updates come from and the GitHub App it signs in with.
//
// The values below are the official tokenmaxr defaults and must match
// collector/release.json (a test enforces it). Anyone shipping their own
// builds writes their own release file; the build tooling stamps every field
// with
//
//	-ldflags "$(go run ./cmd/buildflags release.json)"
//
// which is why these are variables, not constants.
package buildinfo

// Product names the binaries (Product, Product+"w.exe" for the Windows app),
// the state directory and the autostart entries.
const Product = "tokenmaxr"

// LegacyProduct is the name tokenmaxr had as d0m1.com's collector: an install
// under it is migrated once (internal/app/migrate.go).
const LegacyProduct = "d0m1-collector"

var (
	// DefaultEndpoint is the server a new install enrols with unless told
	// otherwise; "" means none (publish to GitHub, or keep numbers local).
	DefaultEndpoint = ""
	// UpdateURL is the signed release manifest (latest.json; latest.json.sig beside it).
	UpdateURL = "https://github.com/7-of-9/tokenmaxr/releases/latest/download/latest.json"
	// DownloadBase prefixes every binary URL a manifest may name: DownloadBase + version + "/".
	DownloadBase = "https://github.com/7-of-9/tokenmaxr/releases/download/v"
	// ReleasePublicKey verifies the manifest signature (raw ed25519 key, base64).
	ReleasePublicKey = "96b7ttqgnW7jfZBTA4TR8rbJjZ7H716WoplKD45W0Gk="

	// GitHubAppSlug and GitHubClientID identify the GitHub App the collector
	// signs in with (device flow: the client id is public, no secret is used).
	GitHubAppSlug  = "tokenmaxor"
	GitHubClientID = "Iv23lisUk4XpDfcdK0Mh"
	// GitHubTemplateRepo is "owner/name" of the template a user's publishing
	// repository is created from.
	GitHubTemplateRepo = "7-of-9/tokenmaxr-pages"
)

// LDFlagNames maps release.json fields to the variables above.
var LDFlagNames = map[string]string{
	"defaultEndpoint":    "DefaultEndpoint",
	"updateUrl":          "UpdateURL",
	"downloadBase":       "DownloadBase",
	"releasePublicKey":   "ReleasePublicKey",
	"githubAppSlug":      "GitHubAppSlug",
	"githubClientId":     "GitHubClientID",
	"githubTemplateRepo": "GitHubTemplateRepo",
}
