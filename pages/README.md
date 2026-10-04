# tokenmaxr usage dashboard

This repository holds AI token usage published by
[tokenmaxr](https://github.com/7-of-9/tokenmaxr) collectors, and a small
static dashboard that GitHub Pages serves from it.

- `data/machines/<id>/` is written by the collectors: one folder per machine
  (`meta.json`, `usage-YYYY-MM.json` daily totals, `quota.json`). Daily totals
  and quota meters only: no prompts, code, file paths, hostnames or account
  emails are ever published.
- `site/` is the dashboard (plain HTML, CSS and JavaScript, no build step).
- `.github/workflows/pages.yml` rebuilds and deploys it on every push.
- `tokenmaxr.json` marks this repository for the collector; set `title` there
  to rename the dashboard.

The dashboard lives at `https://<you>.github.io/<this repository>/`.

Preview locally:

```sh
node scripts/build-index.mjs
mkdir -p _site && cp -r site/. _site/ && cp -r data tokenmaxr.json _site/
cd _site && python -m http.server 8000
```

The collectors keep the fleet key in this repository's Actions variable
`TOKENMAXR_FLEET_KEY` (only collaborators can read it). It lets every machine
hash the same AI account to the same id; do not delete it.
