# tokenmaxr

Count every AI token you burn, on every machine, and publish a dashboard of it
from your own GitHub account. Nothing to host.

tokenmaxr is a small background app (a tray icon on Windows, a menu-bar item on
macOS) that reads the local logs your AI coding tools already write, works out
how many tokens each day cost per provider and model, and publishes daily totals
to a repository in **your** GitHub account. GitHub Pages turns that repository
into a dashboard: a year heatmap, totals, per-model breakdown, per-machine
status and your plans' weekly quota meters.

Supported tools: **Claude Code**, **OpenAI Codex CLI**, **Grok CLI**, **Cursor**
and **Gemini CLI**.

## Quick start

**Windows** (PowerShell):

```powershell
irm https://github.com/7-of-9/tokenmaxr/releases/latest/download/install.ps1 | iex
```

**macOS / Linux**:

```sh
curl -fsSL https://github.com/7-of-9/tokenmaxr/releases/latest/download/install.sh | sh
```

The installer downloads the release for your platform, checks its SHA-256
against the signed release manifest, installs into your user profile (no admin
rights) and starts the app. A settings page then opens in your browser:

1. **Sign in with GitHub.** A one-time code appears; GitHub opens and asks you to
   authorize the **tokenmaxor** app. tokenmaxr never sees your password.
2. **Create your repository.** GitHub opens a pre-filled "new repository from
   template" page (`tokenmaxr-usage`, public). Click *Create repository*.
3. **Install the app on that repository only.** GitHub opens the install page;
   choose *Only select repositories* and pick `tokenmaxr-usage`.

The settings page follows along by itself and finishes when GitHub is ready.
A few minutes later your dashboard is live at
`https://<your-username>.github.io/tokenmaxr-usage/`.

Prefer a terminal? `tokenmaxr github login` does the same three steps.

## More machines

Install tokenmaxr on each machine and sign in with the same GitHub account. Every
machine publishes into its own folder of the same repository, and the
dashboard adds them up. Machines share a *fleet key* (kept in the repository's
Actions variable `TOKENMAXR_FLEET_KEY`, which only collaborators can read), so
the same AI account gets the same anonymous id on every machine.

## What leaves your machine

Only what the dashboard shows, and only to your repository:

| Published | Never published |
| --- | --- |
| Daily token totals per provider, tool, model and anonymous account id | Prompts, responses, code, file names or paths |
| Daily prompt counts | Project or workspace names |
| Quota meters: % of a plan's limit used, plan name, reset time | Account emails, organisation names, API keys |
| A machine label you choose, its OS and the tokenmaxr version | Hostname (the label defaults to a random name) |

Account ids are an HMAC of the provider's account id with your fleet key, so
they cannot be reversed or matched across fleets. The repository is public
(GitHub Pages needs that on free plans); it holds nothing but the above.

tokenmaxr reads logs only from the tools listed above, in your user profile.
It may set Claude Code's `cleanupPeriodDays` and Grok CLI's `cleanup_ttl_days`
so your history is not deleted before it is counted (with a backup;
`--no-fix-config` opts out).

## The app

Click the icon for today's numbers per provider (latest event, last 24 hours,
last 30 days), sync status, the GitHub account and repository you publish to,
and *Settings…*:

- **GitHub**: sign in or out, rename this machine on the dashboard, publish
  every 10 minutes to once a day (default 30 minutes), turn quota meters off.
- **Your own server** (optional): see below.

The app collects every minute, starts at login and updates itself from this
repository's signed releases (checked hourly, verified with an ed25519 key
built into the binary).

## Command line

```
tokenmaxr github login [--label NAME]   publish to your GitHub (guided)
tokenmaxr github status | logout
tokenmaxr settings                      open the settings page of the running app
tokenmaxr status                        local health summary
tokenmaxr doctor                        status plus live checks
tokenmaxr sync-now                      collect and publish in the foreground
tokenmaxr scan --dry-run [--json]       parse all logs and print totals; nothing saved or sent
tokenmaxr uninstall [--purge]           remove autostart and PATH entry (--purge: all local data)
```

`tokenmaxr --help` lists everything. State lives in `%LOCALAPPDATA%\tokenmaxr`
(Windows), `~/Library/Application Support/tokenmaxr` (macOS) or
`~/.local/state/tokenmaxr` (Linux); `--home DIR` or `TOKENMAXR_HOME` override it.

On Linux there is no tray: add the cron line the installer prints, which runs
`tokenmaxr run` every minute.

## Your own server (optional)

tokenmaxr can also send every individual event (and, encrypted, your prompts)
to a server you run, for a private, finer-grained dashboard. `server/` is that
server: Azure Functions with Azure Table Storage, deployable as the managed API
of an Azure Static Web App, with GitHub sign-in for the owner. Connect a machine
with `tokenmaxr install --endpoint https://your-server.example` or from the
settings page; GitHub publishing keeps working alongside it. The server's
protocol is in [docs/SPEC.md](docs/SPEC.md). This mode is for people
comfortable running Azure; the GitHub mode needs none of it.

## Repository layout

| Path | What |
| --- | --- |
| `collector/` | The app and CLI (Go). `release.json` is the release identity built into official binaries. |
| `pages/` | The dashboard template: what your `tokenmaxr-usage` repository is created from (static site, Pages workflow). |
| `server/` | The optional server (Node 22, Azure Functions v4). |
| `install/` | The install scripts published with each release. |
| `docs/SPEC.md` | Collector and server specification. |

## Build from source

```sh
cd collector
go test ./...
go build -ldflags "$(go run ./cmd/buildflags release.json)" ./cmd/tokenmaxr
```

macOS builds need cgo (the menu-bar app); Windows builds also produce
`tokenmaxrw.exe` with `-ldflags "... -H windowsgui"` (the tray app, no console
window). `.github/workflows/release.yml` is the exact release recipe.

To ship your own builds, write your own `release.json` (update URL, download
base, your public signing key, your GitHub App) and sign the release manifest
with `go run ./cmd/signmanifest` (`COLLECTOR_SIGNING_KEY` is the base64 ed25519
seed).

## Uninstall

`tokenmaxr uninstall` (add `--purge` to delete local data too). Revoke the
GitHub app at <https://github.com/settings/installations> and delete the
`tokenmaxr-usage` repository if you want the published data gone.

## History

tokenmaxr started as `d0m1-collector`, the collector behind
[d0m1.com/tokens](https://d0m1.com/tokens). Installs of it move to tokenmaxr by
themselves on their next update (state, enrolment and history included).

## License

MIT. See [LICENSE](LICENSE).
