// Writes data/index.json for the dashboard: the machines under
// data/machines/ and the files each one published. GitHub Pages cannot list
// folders, so the Pages workflow runs this before deploying.
// Usage: node scripts/build-index.mjs [root]   (default: current folder)
import { readdirSync, readFileSync, writeFileSync, statSync, existsSync } from "node:fs";
import { join } from "node:path";

const root = process.argv[2] || ".";
const dir = join(root, "data", "machines");
const ok = /^(meta|quota|account-usage|owner)\.json$|^usage-\d{4}-\d{2}\.json$/;

let title;
try {
  title = JSON.parse(readFileSync(join(root, "tokenmaxr.json"), "utf8")).title;
} catch {}

const machines = existsSync(dir)
  ? readdirSync(dir)
      .filter((id) => /^m_[0-9a-f]{12}$/.test(id) && statSync(join(dir, id)).isDirectory())
      .sort()
      .map((id) => ({ id, files: readdirSync(join(dir, id)).filter((f) => ok.test(f)).sort() }))
      .filter((m) => m.files.length)
  : [];

// Schema 2 lists account-usage.json, schema 3 also owner.json (the owner's plan limits,
// encrypted). Older indexes (repositories made from an earlier template) did not, so the
// dashboard looks for those files itself there (owner.json only once the owner has unlocked it).
const index = { schema: 3, title: typeof title === "string" ? title.slice(0, 120) : undefined, generatedAt: new Date().toISOString(), machines };
writeFileSync(join(root, "data", "index.json"), JSON.stringify(index, null, 1) + "\n");
console.log(`index: ${machines.length} machine(s)`);
