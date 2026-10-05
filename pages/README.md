# tokenmaxr usage dashboard

This repository holds AI token usage published by
[tokenmaxr](https://github.com/7-of-9/tokenmaxr) collectors, and the static
dashboard that GitHub Pages serves from it: the same pages as
[d0m1.com/tokens](https://d0m1.com/tokens), built from the same code, reading
this repository's files, and dressed to look like a page of GitHub's.

It shows tokens and prompts per active day, the number of active days (days
with recorded tokens), a daily heatmap with days that had prompts but no
recorded tokens outlined, exact and estimated tokens, prompts per model, an
API-equivalent cost, a monthly activity feed, machines (with their country's
flag when the owner publishes it) and, under Detail, charts per provider,
model, machine and country. Pick one machine from the menu to see only its
own records. Once the owner signs in (below), the owner also gets the
**Agents** page: every AI account's plan, email and organisation, the weekly
quota left and when it resets, as on d0m1.com/tokens/agents.

The collectors link the dashboard from this repository once: its website
(the About box) when it has none, and an "Open the dashboard" line under this
README's title when the README does not link it yet. Whichever machine does
it records it in `tokenmaxr.json` (`"linked"`, below), and every machine
reads that first, so change or remove either and it stays that way, however
many machines you add later. A website you set yourself is left alone. If
tokenmaxor may not change this repository's settings, the README line still
goes in and the website is tried again once a day. Your GitHub profile is
never touched; to show the dashboard there, add it to your profile README (a
repository named after your account) or your profile's website.

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
    sign-in*). Without the owner key it reveals nothing but its size. It is
    rewritten (with a fresh nonce) only when what it says changes. Collectors
    from 0.4.7 always publish it (there is no option to turn the meters off:
    they are encrypted).
  - `quota.json` was published in the clear by collectors before 0.4.2
    (plans and reset times). The first publish of a newer collector deletes
    every machine's, a retired machine's too, and the dashboard does not
    read it. Git history still holds the old files.
  - `account-usage.json` (Codex, when signed in with a ChatGPT account;
    collectors from 0.4.7 always publish it, and before that it was the
    opt-in `github.showAccountHistory`): the account's daily token totals as Codex reports them (UTC days,
    covering every machine signed into it, with no input/output split), and
    this machine's own Codex tokens per UTC day (`ledger`, read by the names
    in `ledgerCols`). Next to the local-date usage rows, UTC days reveal the
    machine's time-zone offset. The dashboard adds
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
  an older index the signed-in dashboard looks for each machine's `owner.json`
  itself (a 404 means none).
- `tokenmaxr.json` marks this repository for the collector, and holds the
  dashboard's settings (the Pages workflow serves it beside the page):
  - `title` renames the dashboard (the browser tab).
  - `repository` (`"<owner>/<name>"`) names this repository, for a dashboard
    on a custom domain (on `<you>.github.io/<repository>/` the address says
    it). Without it, signing in on a custom domain tries the repositories
    tokenmaxor can reach for that account.
  - `linked` is written by the collectors: `{"readme": true, "website":
    true}` once they have linked the dashboard from this README and from the
    repository's website (above). Each step marked `true` is never done
    again; `"website": false` means tokenmaxor could not set the website yet.
    Set `"linked": true` yourself to keep the collectors from doing either.

  For example:

  ```json
  {
   "tokenmaxr": 1,
   "title": "AI token usage"
  }
  ```

  An older `"background": true` (d0m1.com's animated background) is ignored:
  the dashboard has no background (below).

  Pushing the change redeploys the dashboard.

The dashboard lives at `https://<you>.github.io/<this repository>/`. Its
settings are in the link: `#/?period=90d`, `#/?view=detail`,
`#/?machine=<id>`; the owner's Agents page is `#/agents`.

## Renaming this repository

Rename it on github.com (Settings → General → Repository name). Your
machines follow by themselves; there is nothing to change on any of them:

- GitHub keeps sending the collectors' requests for the old name to the
  repository, and each collector (newer than 0.4.7) notices with its next
  publish that has something to commit (or within a day), takes the new name
  into its settings and publishes there. A machine that publishes with the
  sign-in your fleet shares follows too, by itself or from the share. Its
  status (`tokenmaxr github status`), Settings and the app show the new name
  and dashboard address straight away, and "renamed on GitHub from ..." for a
  week. Nothing is published again: the data is the same repository's.
- **The old dashboard address stops working**: GitHub Pages does not
  redirect `https://<you>.github.io/<old name>/`. The collectors point the
  repository's website and this README's links at the new address (only
  where they named the old one; a website or link of your own stays), and the
  Pages workflow deploys the new address with the next push. Update links you
  shared or bookmarked yourself, and `repository` in `tokenmaxr.json` if you
  set it.
- The owner key a browser keeps is kept per dashboard address: sign in again
  once at the new address (the old one's key stays in the browser until you
  clear the site's data).
- Moving the repository to another account (a transfer) works the same while
  the tokenmaxor App is installed with access to it there; otherwise the
  collectors keep the old name and publishing stops with "cannot write to
  the repository" until you install the App for it (they follow within the
  hour after that) or sign in again on a machine. Do not create a new
  repository with the old name: GitHub then stops redirecting it (the
  collectors find the repository by its id instead, but older collectors do
  not).

## Owner sign-in

Collectors from 0.4.2 publish `owner.json`, encrypted with the owner key.
Signing in puts that key in your browser; there are two ways.

**Sign in with GitHub** (any device, a phone included):

1. Choose **Sign in** in the dashboard's header. The Agents page asks GitHub
   for a code and shows it, large, with **Copy code**.
2. **Open GitHub** opens `https://github.com/login/device` in a new tab and
   copies the code. Paste it there, signed in as the GitHub account that owns
   this repository (or a collaborator), and approve **tokenmaxor**, the
   collector's GitHub App.
3. The dashboard notices by itself. With GitHub's token for that account it
   reads this repository's `TOKENMAXR_FLEET_KEY` (an Actions variable: only
   collaborators can read it, and tokenmaxor must be installed with access to
   this repository), derives the owner key from it in the browser (WebCrypto),
   checks that the key opens the published `owner.json` files, keeps it, and
   forgets the token and the fleet key. It shows the **Agents** tab under the
   header and **Sign out** in it.

GitHub's device-flow endpoints cannot be called from a web page (they send no
CORS headers), so the page reaches them through a small relay on d0m1.com
(`https://d0m1.com/api/github/device/code` and `.../token`). It forwards exactly
those two requests with tokenmaxor's public client id and nothing else; there is
no client secret. The relay passes GitHub's answer back, the token included, and
keeps and logs none of it. The page sends the token only to `api.github.com`.
The key never leaves the browser. What can go wrong is said in so many words:
an expired code (get a new one), sign-in cancelled on GitHub, "This GitHub
account can't read `<repository>`", "tokenmaxor isn't installed with access to
`<repository>`" (add the repository to the App's installation), or GitHub
unreachable.

**Open dashboard** in the collector's settings page (on a machine you own,
while it publishes to GitHub and the dashboard has its address) opens the
dashboard at `#unlock` and hands it the key by `postMessage`, so the key is
never in an address or the browser's history. (**Copy sign-in link (for
another browser)**, under Advanced, gives
`https://<you>.github.io/<this repository>/#unlock=<key>` instead; that link
does stay in that browser's history. The `#unlock` names are the collector's.)

Either way:

1. The dashboard turns the key into a WebCrypto key that cannot be read back
   out (non-extractable) and keeps that in this browser's IndexedDB for this
   dashboard. A link's key leaves the address bar at once.
2. From then on the browser fetches each machine's `owner.json` and decrypts
   it locally (WebCrypto, AES-256-GCM). The key is never sent anywhere: no
   request carries it, and fragments (`#...`) are never sent to a server.
   The Agents page's choices (which accounts are tracked, by email) are kept
   encrypted with the same key.
3. **Sign out** shows the public view (what any visitor sees) and **Sign in**
   brings yours back, in this browser and its other tabs of this dashboard.
   Signing out keeps the key, so signing in again takes one click. In a
   browser without the key, **Sign in** starts the GitHub sign-in. To remove
   the key from a browser, clear this site's data (the browser's site settings
   for `<you>.github.io`).

The key is derived from the fleet key (`TOKENMAXR_FLEET_KEY`, 32 bytes in
standard base64): HMAC-SHA256(fleet key, `tokenmaxr dashboard owner v1`). The
collectors encrypt with it. Each file is bound to its machine (the AES-GCM
additional data is `tokenmaxr dashboard owner v1|<machine id>`), so a file
moved between machines, altered, or opened with another key does not open,
and the page shows the public view rather than failing. Anyone who has the key
(or the sign-in link) can read the owner's accounts, now and in every later
`owner.json`: treat the link like a password. Whoever can read the fleet key
(the repository's collaborators) can derive it, which is what the GitHub
sign-in does.

What the browser keeps, and who else could reach it:

- **The sign-in link stays in the browser's history.** Taking the key out of
  the address bar does not remove the visit the browser has already recorded,
  and a browser that syncs its history copies the link to your other devices
  and offers it as an address-bar suggestion. Sign out does not remove it
  either. After using one, delete that history entry (search history for
  `unlock=`), or use it in a browser profile that does not sync history. The
  GitHub sign-in leaves nothing in the history.
- **Every Pages site of your account shares one origin** (`<you>.github.io`),
  and with it the storage this dashboard uses. Any script on any of those
  sites (another repository's page, its analytics or a library it loads)
  could use the stored key to decrypt `owner.json` while this browser holds
  it. Because the key is non-extractable, it cannot copy the key out to use
  after the key is gone. To keep the dashboard on an origin of its own, give
  this repository's Pages a custom domain; otherwise sign in only in a
  browser profile that does not visit your other Pages sites, and clear this
  site's data when done (Sign out keeps the key).

## Differences from d0m1.com/tokens

The dashboard has d0m1.com/tokens' content: the same pages (Overview and
Detail, the Agents page), the same data and layout, the same slide between
pages. It looks like a page of GitHub's rather than of d0m1.com: GitHub's
colours (Primer), light or dark as your system is set, the system font,
GitHub's boxes, buttons and segmented controls, and the contribution graph's
greens; no background video or image, no d0m1.com faces or glow
(`pages/dashboard/github.css`, scoped to the dashboard's page, so d0m1.com is
unchanged). d0m1.com's site footer (its copyright and links) is hidden on its
token pages, so the dashboard leaves it out. What differs on purpose:

- GitHub's header bar in place of d0m1.com's `< d0m1 / tokens`: the GitHub
  mark (to github.com), then `<you> / <repository>` as GitHub writes it, each
  linking to it on GitHub, and Sign in or Sign out on the right
  (`GithubHeader.tsx`). On a custom domain the repository comes from
  `repository` in `tokenmaxr.json`; without one the header shows the
  dashboard's title, linking to the dashboard. The header scrolls with the
  page, as GitHub's does. Below it, once you are signed in (or on the Agents
  page), the repository-style tabs Usage and Agents. Esc goes back a page.
- The owner signs in with GitHub's device flow (a code to enter at
  github.com/login/device) or from the collector, and the browser keeps a key
  rather than a session; Sign out and Sign in then toggle the public view.
- There is no Prompts page: prompt text is never published.
- The data footer names this repository as the source, and the browser tab
  says `<title> · tokenmaxr`, with no favicon.

Preview locally:

```sh
node scripts/build-index.mjs
mkdir -p _site && cp -r site/. _site/ && cp -r data tokenmaxr.json _site/
cd _site && python -m http.server 8000
```

The collectors keep the fleet key in this repository's Actions variable
`TOKENMAXR_FLEET_KEY` (only collaborators can read it). It lets every machine
hash the same AI account to the same id; do not delete it.
