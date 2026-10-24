// Writes data/index.json for the deployed dashboard: the machines under
// data/machines/ and the files each one published. GitHub Pages cannot list
// folders, so the Pages workflow runs this before deploying. (Newer collectors
// also commit a data/index.json in this format with every data commit, which a
// public repository's dashboard reads live; this one, written over it in the
// deployed copy, adds the title and the build time.)
// Usage: node scripts/build-index.mjs [root]   (default: current folder)
// SOURCE_TREE (the workflow sets it: git's id for the tree of the commit
// deployed) is kept as "sourceTree", so a scheduled run can tell whether
// anything changed since.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync, statSync, existsSync } from "node:fs";
import { join } from "node:path";

const root = process.argv[2] || ".";
const dir = join(root, "data", "machines");
// quota.json (published in the clear before collector 0.4.2) is not listed:
// the dashboard does not read it, and newer collectors delete it.
const ok = /^(meta|account-usage|owner)\.json$|^usage-\d{4}-\d{2}\.json$/;

let title;
try {
  title = JSON.parse(readFileSync(join(root, "tokenmaxr.json"), "utf8")).title;
} catch {}

// git's object id for a file (`git hash-object`): the dashboard reads a file
// again only when its id changes.
const blob = (path) => {
  const bytes = readFileSync(path);
  return createHash("sha1").update(`blob ${bytes.length}\0`).update(bytes).digest("hex");
};

const machines = existsSync(dir)
  ? readdirSync(dir)
      .filter((id) => /^m_[0-9a-f]{12}$/.test(id) && statSync(join(dir, id)).isDirectory())
      .sort()
      .map((id) => {
        const files = readdirSync(join(dir, id)).filter((f) => ok.test(f)).sort();
        return { id, files, blobs: Object.fromEntries(files.map((f) => [f, blob(join(dir, id, f))])) };
      })
      .filter((m) => m.files.length)
  : [];

// Schema 2 lists account-usage.json, schema 3 also owner.json (the owner's plan limits,
// encrypted), schema 4 each file's git object id ("blobs"). Older indexes (repositories made
// from an earlier template) did not list those files, so the dashboard looks for them itself
// there (owner.json only once the owner has unlocked it), and reads every file each time.
const sourceTree = /^[0-9a-f]{40,64}$/.test(process.env.SOURCE_TREE || "") ? process.env.SOURCE_TREE : undefined;
const index = { schema: 4, title: typeof title === "string" ? title.slice(0, 120) : undefined, generatedAt: new Date().toISOString(), sourceTree, machines };
writeFileSync(join(root, "data", "index.json"), JSON.stringify(index, null, 1) + "\n");
console.log(`index: ${machines.length} machine(s)`);
