# tokenmaxr usage dashboard

This repository holds AI token usage published by
[tokenmaxr](https://github.com/7-of-9/tokenmaxr) collectors, and the static
dashboard that GitHub Pages serves from it: the same pages as
[d0m1.com/tokens](https://d0m1.com/tokens), built from the same code, reading
this repository's files.

It shows tokens and prompts per active day, the number of active days (days
with recorded tokens), a daily heatmap with days that had prompts but no
recorded tokens outlined, exact and estimated tokens, prompts per model, an
API-equivalent cost, a monthly activity feed, machines (with their country's
flag when the owner publishes it) and, under Detail, charts per provider,
model, machine and country. Pick one machine from the menu to see only its
own records. Once the owner unlocks it (below), the owner also gets the
**Agents** page: every AI account's plan, email and organisation, the weekly
quota left and when it resets, as on d0m1.com/tokens/agents.

- `data/machines/<id>/` is written by the collectors, one folder per machine:
  - `meta.json`: the machine's public label, OS, collector version, when it
    last published, its first data day and newest event; its country only if
    the owner turned on `github.showCountry` in the collector config or
    settings page (off by default; a machine that publishes with the sign-in
    another machine shared follows that machine's choice, unless it was set
    on the machine itself).
  - `usage-YYYY-MM.json`: daily totals per provider, tool, model and account
    hash. Rows are read by the names in `cols`, so older files keep working.
    Prompt counts include prompts whose tokens were never recorded
    (`promptsNoUsage`) and, per model, prompts recorded for it
    (`modelPrompts`, a count; never the text).
  - `owner.json`: the owner's plan limits (the rows d0m1.com's owner-only
    `GET /api/limits` returns: account email, organisation, plan, each
    window's use and reset), **encrypted** for the owner (see *Owner
    unlock*). Without the owner key it reveals nothing but its size. It is
    rewritten (with a fresh nonce) only when what it says changes, and
    removed when the owner turns quota meters off.
  - `quota.json` was published in the clear by collectors before 0.4.2
    (plans and reset times). The first publish of a newer collector deletes
    every machine's, a retired machine's too, and the dashboard does not
    read it. Git history still holds the old files.
  - `account-usage.json` (Codex, when signed in with a ChatGPT account, and
    only if the owner turned on `github.showAccountHistory` in the collector
    config or settings page; off by default, and turning it off deletes the
    file): the account's daily token totals as Codex reports them (UTC days,
    covering every machine signed into it, with no input/output split), and
    this machine's own Codex tokens per UTC day (`ledger`, read by the names
    in `ledgerCols`). Next to the local-date usage rows, UTC days reveal the
    machine's time-zone offset, which is why it is opt-in. The dashboard adds
    only the part of each total that no machine recorded locally, shown as
    **account history**, exactly as d0m1.com reconciles it. The account counts
    per account and day, not per machine, so the machine and region panels
    place that part on the machines whose Codex ran that day (or the nearest
    days), first on those whose prompts that day have no token records, then
    by tokens, marked "est." with the estimated amount in the hover; it is
    never shown as an unknown machine while any machine has used Codex. Totals that might
    overlap tokens of a machine that publishes no ledger (an older collector
    or one that has not opted in), or of a local date it lists in
    `unledgered` (totals kept from an older collector's records, whose UTC
    days are unknown), wait, as "awaiting reconciliation" in the footer;
    totals that contradict the local records are left out and the footer
    says so.

  Only daily totals are in the clear: no prompts, code, file paths or
  hostnames are ever published, and plans, quota meters, account emails and
  organisation names only inside the encrypted `owner.json`. The dashboard shows accounts
  as per-page aliases (`acct1`, `acct2`, ...), never their hashes.

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
  `scripts/build-index.mjs` writes schema 3, which lists `owner.json`; under
  an older index the unlocked dashboard looks for each machine's `owner.json`
  itself (a 404 means none).
- `tokenmaxr.json` marks this repository for the collector; set `title` there
  to rename the dashboard.

The dashboard lives at `https://<you>.github.io/<this repository>/`. Its
settings are in the link: `#/?period=90d`, `#/?view=detail`,
`#/?machine=<id>`; the owner's Agents page is `#/agents`.

## Owner unlock

Pages has no sign-in, so the owner's page is unlocked with a key instead.
Collectors from 0.4.2 publish `owner.json` and offer the unlock on their
Settings page (while they publish to GitHub and the dashboard has its
address).

1. On a machine you own, open the collector's Settings page and choose
   **Open my dashboard (unlocked)**. It opens the dashboard at `#unlock` and
   hands it the key by `postMessage`, so the key is never in an address or
   the browser's history. (**Copy unlock link**, for a phone or another
   browser, gives `https://<you>.github.io/<this repository>/#unlock=<key>`
   instead; that link does stay in that browser's history.)
2. The dashboard takes the key out of the address bar at once, turns it into
   a WebCrypto key that cannot be read back out (non-extractable), keeps that
   in this browser's IndexedDB for this dashboard, and shows **Agents** and
   **Sign out** in the header, as d0m1.com does.
3. From then on the browser fetches each machine's `owner.json` and decrypts
   it locally (WebCrypto, AES-256-GCM). The key is never sent anywhere: no
   request carries it, and fragments (`#...`) are never sent to a server.
   The Agents page's choices (which accounts are tracked, by email) are kept
   encrypted with the same key.
4. **Sign out** shows the public view (what any visitor sees) and **Sign in**
   brings yours back, in this browser and its other tabs of this dashboard.
   Signing out keeps the key, so signing in takes one click; in a browser
   never unlocked, **Sign in** opens the Agents page, which says how to
   unlock it. To remove the key from a browser, clear this site's data
   (the browser's site settings for `<you>.github.io`).

The key is derived from the fleet key (`TOKENMAXR_FLEET_KEY`):
HMAC-SHA256(fleet key, `tokenmaxr dashboard owner v1`). The collectors encrypt
with it; the page never sees the fleet key itself. Each file is bound to its
machine (the AES-GCM additional data is `tokenmaxr dashboard owner v1|<machine
id>`), so a file moved between machines, altered, or opened with another key
does not open, and the page stays locked rather than failing. Anyone who has
the key (or the unlock link) can read the owner's accounts, now and in every
later `owner.json`: treat the link like a password. Whoever can read the fleet
key (the repository's collaborators) can derive it.

What the browser keeps, and who else could reach it:

- **The unlock link stays in the browser's history.** Taking the key out of
  the address bar does not remove the visit the browser has already recorded,
  and a browser that syncs its history copies the link to your other devices
  and offers it as an address-bar suggestion. Sign out does not remove it either.
  After unlocking, delete that history entry (search history for `unlock=`),
  or unlock in a browser profile that does not sync history.
- **Every Pages site of your account shares one origin** (`<you>.github.io`),
  and with it the storage this dashboard uses. Any script on any of those
  sites (another repository's page, its analytics or a library it loads)
  could use the stored key to decrypt `owner.json` while this browser is
  unlocked. Because the key is non-extractable, it cannot copy the key out to
  use after you lock. To keep the dashboard on an origin of its own, give
  this repository's Pages a custom domain; otherwise unlock only in a
  browser profile that does not visit your other Pages sites, and clear this
  site's data when done (Sign out keeps the key).

## Differences from d0m1.com/tokens

The pages, shell and styles are d0m1.com's own, without an outer panel on
either site. What differs on purpose: the breadcrumb starts with the GitHub
user (`<you>` of `<you>.github.io`; elsewhere the dashboard's title, cut short
when long) where d0m1.com has "d0m1", and `<` does nothing on the first page
(there is no home page above it); the owner gets in with the unlock link
rather than a GitHub sign-in (Sign out and Sign in then toggle the public
view); there is no Prompts page (prompt text is never published); and
the page sits on plain black, without d0m1.com's background video, theme
controls, page-slide transitions or site footer.

Preview locally:

```sh
node scripts/build-index.mjs
mkdir -p _site && cp -r site/. _site/ && cp -r data tokenmaxr.json _site/
cd _site && python -m http.server 8000
```

The collectors keep the fleet key in this repository's Actions variable
`TOKENMAXR_FLEET_KEY` (only collaborators can read it). It lets every machine
hash the same AI account to the same id; do not delete it.
