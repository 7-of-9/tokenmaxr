# tokenmaxr usage dashboard

This repository holds AI token usage published by
[tokenmaxr](https://github.com/7-of-9/tokenmaxr) collectors, and the static
dashboard that GitHub Pages serves from it: the same page as
[d0m1.com/tokens](https://d0m1.com/tokens), reading this repository's files.

It shows tokens and prompts per active day, the number of active days (days
with recorded tokens), a daily heatmap with days that had prompts but no
recorded tokens outlined, exact and estimated tokens, prompts per model, an
API-equivalent cost, a monthly activity feed, machines, plan quota meters and,
under Detail, charts per provider, model, machine and country. Pick one
machine from the menu to see only its own records.

- `data/machines/<id>/` is written by the collectors, one folder per machine:
  - `meta.json`: the machine's public label, OS, collector version, when it
    last published, its first data day and newest event; its country only if
    the owner turned on `github.showCountry` in the collector config (off by
    default).
  - `usage-YYYY-MM.json`: daily totals per provider, tool, model and account
    hash. Rows are read by the names in `cols`, so older files keep working.
    Prompt counts include prompts whose tokens were never recorded
    (`promptsNoUsage`) and, per model, prompts recorded for it
    (`modelPrompts`, a count; never the text).
  - `quota.json`: plan quota meters as each tool last reported them.
  - `account-usage.json` (Codex, when signed in with a ChatGPT account, and
    only if the owner turned on `github.showAccountHistory` in the collector
    config or settings page; off by default, and turning it off deletes the
    file): the account's daily token totals as Codex reports them (UTC days,
    covering every machine signed into it, with no input/output split), and
    this machine's own Codex tokens per UTC day (`ledger`, read by the names
    in `ledgerCols`). Next to the local-date usage rows, UTC days reveal the
    machine's time-zone offset, which is why it is opt-in. The dashboard adds
    only the part of each total that no machine recorded locally, shown as
    **account history**, exactly as d0m1.com reconciles it. Totals that might
    overlap tokens of a machine that publishes no ledger (an older collector
    or one that has not opted in), or of a local date it lists in
    `unledgered` (totals kept from an older collector's records, whose UTC
    days are unknown), wait, as "awaiting reconciliation" in the footer;
    totals that contradict the local records are left out and the footer
    says so.

  Daily totals and quota meters only: no prompts, code, file paths, hostnames
  or account emails are ever published. The dashboard shows accounts as
  per-page aliases (`acct1`, `acct2`, ...), never their hashes.

  Each machine publishes totals of the logs it reads, and the dashboard adds
  the machines up. Two collectors that read the same logs (a Windows
  collector that also scans a WSL distro's home, plus a collector inside that
  distro, or a home folder synced between machines) count them twice here,
  where d0m1.com's server counts each event once. Run one collector per set
  of logs (on Windows, set `discoverWsl` to false in the collector config
  where a distro runs its own collector).
- `site/` is the built dashboard, and it updates itself: every collector
  carries the dashboard of its release, and when that build is newer than
  this repository's (`site/version.json`, compared by `builtAt`) it commits
  the new `site/` files and deletes the old ones there, in one commit. It
  never touches anything outside `site/`, and a machine on an older release
  never replaces a newer dashboard. Each collector checks once a day, and at
  once after the collector itself updates. Do not edit `site/` by hand: the next release
  replaces it (a `version.json` the collector cannot read is left alone).

  In the tokenmaxr repository, `npm run build:pages` builds
  `pages/dashboard/` into `pages/site/`, then
  `scripts/sync-pages-site.mjs` writes `pages/site/version.json` (the build
  time and a hash of the files; the time changes only when the files do) and
  copies the folder to `collector/internal/ghpub/site/`, which the collector
  embeds (Go's `go:embed` cannot reach outside the collector module).
  Commit both copies. `npm run check:pages` and the collector's Go tests
  fail when the two differ or `version.json` does not match the files.
- `.github/workflows/pages.yml` rebuilds `data/index.json` (the list of
  machines and files; Pages cannot list folders) and deploys on every push.
- `tokenmaxr.json` marks this repository for the collector; set `title` there
  to rename the dashboard.

The dashboard lives at `https://<you>.github.io/<this repository>/`. Its
settings are in the link: `#/?period=90d`, `#/?view=detail`,
`#/?machine=<id>`.

Preview locally:

```sh
node scripts/build-index.mjs
mkdir -p _site && cp -r site/. _site/ && cp -r data tokenmaxr.json _site/
cd _site && python -m http.server 8000
```

The collectors keep the fleet key in this repository's Actions variable
`TOKENMAXR_FLEET_KEY` (only collaborators can read it). It lets every machine
hash the same AI account to the same id; do not delete it.
