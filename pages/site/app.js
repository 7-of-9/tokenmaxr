// tokenmaxr dashboard: reads the static files the collectors publish
// (data/index.json lists them; the Pages workflow writes it) and renders
// totals, a daily heatmap, quota meters and per-model / per-machine tables.
// No build step, no dependencies, no network calls beyond this site.
"use strict";

const COLS = ["date", "provider", "source", "model", "acct", "in", "cacheW", "cacheR", "out", "events", "prompts"];
const STALE_MS = 6 * 3600 * 1000;
const MAX_AGE_MS = 7 * 86400 * 1000; // older meters say nothing about now
const $ = (id) => document.getElementById(id);

const fmt = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const full = new Intl.NumberFormat();
const tok = (n) => (n < 10000 ? full.format(n) : fmt.format(n));
const total = (c) => c.in + c.cacheW + c.cacheR + c.out;
const blank = () => ({ in: 0, cacheW: 0, cacheR: 0, out: 0, events: 0, prompts: 0 });
const add = (a, b) => { for (const k in a) a[k] += b[k] || 0; return a; };

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function ago(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!t) return "never";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

function until(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!t) return "";
  const s = Math.round((t - now) / 1000);
  if (s <= 0) return "resets now";
  if (s < 5400) return "resets in " + Math.round(s / 60) + " min";
  if (s < 129600) return "resets in " + Math.round(s / 3600) + " h";
  return "resets in " + Math.round(s / 86400) + " days";
}

// Days as local YYYY-MM-DD: collectors bucket by the machine's local day.
const pad = (n) => String(n).padStart(2, "0");
const day = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const addDays = (d, n) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);

async function getJSON(path) {
  const r = await fetch(path, { cache: "no-cache" });
  if (!r.ok) throw new Error(path + ": " + r.status);
  return r.json();
}

async function load() {
  const index = await getJSON("data/index.json");
  const machines = [];
  const rows = [];
  const meters = [];
  await Promise.all((index.machines || []).map(async (m) => {
    const base = "data/machines/" + encodeURIComponent(m.id) + "/";
    const files = m.files || [];
    const meta = files.includes("meta.json") ? await getJSON(base + "meta.json").catch(() => null) : null;
    machines.push(meta || { id: m.id, label: m.id });
    await Promise.all(files.map(async (f) => {
      if (/^usage-\d{4}-\d{2}\.json$/.test(f)) {
        const u = await getJSON(base + f).catch(() => null);
        if (!u || !Array.isArray(u.rows)) return;
        const cols = Array.isArray(u.cols) ? u.cols : COLS;
        for (const r of u.rows) {
          const o = { machine: m.id };
          cols.forEach((c, i) => (o[c] = r[i]));
          rows.push(o);
        }
      } else if (f === "quota.json") {
        const q = await getJSON(base + f).catch(() => null);
        for (const mt of (q && q.meters) || []) meters.push({ ...mt, machine: m.id });
      }
    }));
  }));
  return { title: index.title, generatedAt: index.generatedAt, machines, rows, meters };
}

// One meter per provider/source/account/window/scope: the newest reading
// wins when several machines share an account.
function latestMeters(meters) {
  const by = new Map();
  for (const m of meters) {
    const k = [m.provider, m.source, m.acct, m.window, m.scope].join("|");
    const cur = by.get(k);
    if (!cur || Date.parse(m.observedAt) > Date.parse(cur.observedAt)) by.set(k, m);
  }
  return [...by.values()].sort((a, b) => (b.usedPercent ?? -1) - (a.usedPercent ?? -1));
}

const state = { data: null, range: 30 };

function render() {
  const { data, range } = state;
  const now = new Date();
  const today = day(now);
  const from = range ? day(addDays(now, -(range - 1))) : "0000";
  const inRange = data.rows.filter((r) => r.date >= from && r.date <= today);

  document.querySelectorAll(".ranges button").forEach((b) => b.setAttribute("aria-pressed", String(+b.dataset.range === range)));
  $("range-label").textContent = range ? "· last " + (range === 365 ? "12 months" : range + " days") : "· all time";

  // Tiles.
  const sum = inRange.reduce((a, r) => add(a, r), blank());
  const days = new Set(inRange.filter((r) => total(r) > 0).map((r) => r.date)).size;
  const tiles = [
    ["Tokens", tok(total(sum))],
    ["Input", tok(sum.in + sum.cacheW)],
    ["Output", tok(sum.out)],
    ["Cache reads", tok(sum.cacheR)],
    ["Prompts", full.format(sum.prompts)],
    ["Active days", full.format(days)],
  ];
  $("tiles").innerHTML = tiles.map(([k, v]) => `<div class="tile"><div class="k">${k}</div><div class="v">${v}</div></div>`).join("");

  renderHeatmap(data.rows, now);
  renderQuota(data.meters, now);
  renderModels(inRange, sum);
  renderMachines(data.machines, now);
}

function renderHeatmap(rows, now) {
  const perDay = new Map();
  for (const r of rows) perDay.set(r.date, (perDay.get(r.date) || 0) + total(r));
  // 53 weeks ending this week, Sunday-first columns.
  const end = addDays(now, 0);
  const start = addDays(end, -(52 * 7 + end.getDay()));
  const values = [...perDay.values()].filter((v) => v > 0).sort((a, b) => a - b);
  const q = (p) => values[Math.min(values.length - 1, Math.floor(p * values.length))] || 0;
  const cuts = [q(0.25), q(0.5), q(0.75)];
  const level = (v) => (v <= 0 ? 0 : v <= cuts[0] ? 1 : v <= cuts[1] ? 2 : v <= cuts[2] ? 3 : 4);
  let html = "";
  for (let d = start; d <= addDays(end, 6 - end.getDay()); d = addDays(d, 1)) {
    const k = day(d);
    if (d > end) { html += `<i class="out"></i>`; continue; }
    const v = perDay.get(k) || 0;
    html += `<i class="l${level(v)}" data-tip="${k}: ${v ? tok(v) + " tokens" : "no usage"}"></i>`;
  }
  $("heatmap").innerHTML = html;
}

function renderQuota(all, now) {
  const meters = latestMeters(all.filter((m) => now - Date.parse(m.observedAt) <= MAX_AGE_MS));
  $("quota-card").hidden = meters.length === 0;
  $("quota").innerHTML = meters.map((m) => {
    // Reset since it was read: what is used now is unknown.
    const reset = Date.parse(m.resetsAt) <= now;
    const pct = reset ? null : m.usedPercent;
    const color = pct == null ? "var(--muted)" : pct >= 90 ? "var(--bad)" : pct >= 70 ? "var(--warn)" : "var(--good)";
    const stale = now - Date.parse(m.observedAt) > STALE_MS;
    const name = [m.source || m.provider, m.window, m.scope].filter(Boolean).join(" · ");
    return `<div class="meter">
      <div class="who"><b>${esc(name)}</b>${m.plan ? `<span class="pill">${esc(m.plan)}</span>` : ""}${m.acct ? `<span class="pill" title="Account hash">${esc(m.acct.slice(0, 8))}</span>` : ""}</div>
      <div><span class="pct">${pct == null ? "–" : Math.round(pct) + "%"}</span>
        <div class="gauge"><span style="width:${Math.min(100, Math.max(0, pct || 0))}%;background:${color}"></span></div></div>
      <div class="reset">${reset ? "reset since read" : esc(until(m.resetsAt, now))}<br><span class="${stale ? "stale" : "muted"}">read ${esc(ago(m.observedAt, now))}</span></div>
    </div>`;
  }).join("");
}

function renderModels(rows, sum) {
  const by = new Map();
  for (const r of rows) {
    const k = r.provider + "\u0000" + r.model;
    if (!by.has(k)) by.set(k, { provider: r.provider, model: r.model, c: blank() });
    add(by.get(k).c, r);
  }
  // Prompt counts carry no model; they are in the tiles, not this table.
  const list = [...by.values()].filter((m) => total(m.c) > 0).sort((a, b) => total(b.c) - total(a.c));
  const all = total(sum) || 1;
  $("models").tBodies[0].innerHTML = list.length ? list.map(({ provider, model, c }) => {
    const share = (100 * total(c)) / all;
    return `<tr><td>${esc(provider)}</td><td>${esc(model || "unknown")}</td><td class="num">${tok(total(c))}</td>
      <td class="share"><div class="bar" title="${share.toFixed(1)}%"><span style="width:${share}%"></span></div></td>
      <td class="num">${tok(c.in + c.cacheW)}</td><td class="num">${tok(c.out)}</td><td class="num">${tok(c.cacheR)}</td></tr>`;
  }).join("") : `<tr><td colspan="7" class="empty">No usage in this period.</td></tr>`;
}

function renderMachines(machines, now) {
  const list = [...machines].sort((a, b) => (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0));
  $("machines").tBodies[0].innerHTML = list.length ? list.map((m) =>
    `<tr><td>${esc(m.label || m.id)}</td><td>${esc(m.os || "")}</td><td>${esc(m.collector || "")}</td><td>${esc(ago(m.updatedAt, now))}</td></tr>`
  ).join("") : `<tr><td colspan="4" class="empty">No machine has published yet.</td></tr>`;
}

function tooltips() {
  const tip = $("tip");
  $("heatmap").addEventListener("mousemove", (e) => {
    const t = e.target.dataset && e.target.dataset.tip;
    if (!t) { tip.hidden = true; return; }
    tip.textContent = t;
    tip.hidden = false;
    tip.style.left = Math.min(e.clientX + 12, innerWidth - tip.offsetWidth - 8) + "px";
    tip.style.top = e.clientY + 14 + "px";
  });
  $("heatmap").addEventListener("mouseleave", () => (tip.hidden = true));
}

async function main() {
  const r = +(location.hash.slice(1) || NaN);
  if ([0, 7, 30, 90, 365].includes(r)) state.range = r;
  document.querySelectorAll(".ranges button").forEach((b) => b.addEventListener("click", () => {
    state.range = +b.dataset.range;
    history.replaceState(null, "", "#" + state.range);
    if (state.data) render();
  }));
  tooltips();
  try {
    state.data = await load();
  } catch (e) {
    $("subtitle").textContent = "No data yet: once a collector publishes, this page fills in (the first build takes a few minutes).";
    $("tiles").innerHTML = "";
    console.error(e);
    return;
  }
  if (state.data.title) {
    $("title").textContent = state.data.title;
    document.title = state.data.title + " · tokenmaxr";
  }
  const n = state.data.machines.length;
  $("subtitle").textContent = `${n} machine${n === 1 ? "" : "s"} · built ${ago(state.data.generatedAt)}`;
  render();
  // The page is static; re-render each minute so "ago" labels stay honest.
  setInterval(render, 60000);
}

main();
