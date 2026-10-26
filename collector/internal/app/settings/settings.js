// tokenmaxr settings page: the basics on top (Open dashboard, Stop
// publishing or Connect GitHub…, the display mode), everything else under
// Advanced, closed unless the address asks for it (#github, #advanced).
// It polls api/state and posts changes. Everything is same-origin JSON; the
// page never sees a token or key, apart from the dashboard's owner key,
// fetched only to sign the owner in to the dashboard (below).
"use strict";

const $ = (id) => document.getElementById(id);
const show = (id, on) => { $(id).hidden = !on; };
let state = null;
let timer = 0;
let gone = false; // the app quit or restarted: its server (and this page's) went with it
let misses = 0;
const dirty = new Set(); // inputs the user is editing: polling leaves them alone

// Offline is a fetch that never reached the server (not an error reply).
class Offline extends Error {}

async function api(path, body) {
  const opts = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  let r;
  try {
    r = await fetch("api/" + path, opts);
  } catch {
    throw new Offline("tokenmaxr is not reachable");
  }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.status + " " + r.statusText);
  return j;
}

// goneNow shows the calm "not running, or restarted" state in place of the
// page's controls: the app quit or restarted, and its server went with it
// for good (a next one has a new address, reached again through the app's
// Settings > Advanced…).
function goneNow() {
  if (gone) return;
  gone = true;
  clearTimeout(timer);
  document.body.classList.add("offline");
  $("page").setAttribute("aria-hidden", "true");
  for (const el of document.querySelectorAll("#page button, #page input, #page select")) el.disabled = true;
  show("gone", true);
}

function ago(iso) {
  const t = Date.parse(iso || "");
  if (!t) return "never";
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 90) return "just now";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

function link(el, url, text) {
  el.textContent = text || url;
  if (/^https:\/\//.test(url)) el.href = url; else el.removeAttribute("href");
}

function setInput(id, value) {
  const el = $(id);
  if (dirty.has(id) || document.activeElement === el) return;
  if (el.type === "checkbox") el.checked = !!value; else el.value = value ?? "";
}

function render() {
  const s = state;
  if (!s || gone) return;
  const version = s.version === "dev" ? "dev build" : "v" + s.version;
  $("who").textContent = [s.machine || "This machine", version, s.setUp ? "" : "not set up"].filter(Boolean).join(" · ");
  $("foot").textContent = (s.fleet ? "Fleet " + s.fleet + " · " : "") + version + (s.buildTime ? " · built " + s.buildTime.replace("T", " ").replace(/:\d\dZ$/, " UTC") : "");

  const g = s.github, l = s.login;
  const signing = !!(l && l.running);
  const connecting = !!(s.connect && s.connect.running);

  // The basics.
  show("gh-dashboard", g.on && /^https:\/\//.test(g.pagesUrl || ""));
  show("gh-signout", g.on);
  show("gh-connect", !g.on);
  $("gh-connect").textContent = signing ? "Signing in to GitHub…" : "Connect GitHub…";
  // A sign-in or connection in progress shows its steps.
  if ((signing || connecting) && !$("advanced").open) $("advanced").open = true;

  // GitHub.
  show("gh-off", !g.on && !signing);
  show("gh-off-fleet", !g.on && !!g.fleet);
  $("gh-off-fleet").textContent = g.on ? "" : g.fleet || "";
  show("gh-off-fleet-error", !g.on && !!g.fleetError);
  $("gh-off-fleet-error").textContent = g.on ? "" : g.fleetError || "";
  show("gh-login", signing || !!(l && l.error && !g.on));
  show("gh-on", g.on && !signing);
  const badge = $("gh-badge");
  badge.className = "badge" + (g.on ? (g.lastError ? " err" : " on") : "");
  badge.textContent = signing ? "signing in…" : g.on ? (g.lastError ? "error" : "publishing") : "off";
  if (l) {
    show("gh-code-box", !!l.code && signing && !l.step);
    $("gh-code").textContent = l.code || "";
    link($("gh-code-link"), l.codeUrl || "https://github.com/login/device", "Open " + (l.codeUrl || "https://github.com/login/device").replace(/^https:\/\//, ""));
    show("gh-step-box", !!l.step && signing);
    $("gh-step").textContent = l.step || "";
    link($("gh-step-link"), l.stepUrl || "", "Open it");
    const ul = $("gh-progress");
    ul.replaceChildren(...(l.progress || []).map((p) => Object.assign(document.createElement("li"), { textContent: p })));
    show("gh-login-error", !!l.error);
    $("gh-login-error").textContent = l.error || "";
    $("gh-cancel").textContent = signing ? "Cancel" : "Back";
  }
  if (g.on) {
    link($("gh-repo"), "https://github.com/" + g.repo, g.repo);
    $("gh-who").textContent = (g.adopted ? "(shared by " + g.login + ")" : "(" + g.login + ")") +
      (g.renamedFrom ? " · renamed from " + g.renamedFrom + " on GitHub, followed by itself" : "");
    const pages = $("gh-pages");
    if (g.pagesUrl) {
      const a = document.createElement("a");
      a.target = "_blank"; a.rel = "noopener";
      link(a, g.pagesUrl, g.pagesUrl.replace(/^https:\/\//, ""));
      pages.replaceChildren(a);
    } else {
      pages.textContent = "building (a few minutes)";
    }
    const every = $("gh-every").querySelector(`option[value="${g.publishEveryMinutes}"]`);
    $("gh-last").textContent = ago(g.lastPublish) + " · every " + (every ? every.textContent : g.publishEveryMinutes + " minutes");
    show("gh-error", !!g.lastError);
    $("gh-error").textContent = g.lastError || "";
    show("gh-fleet", !!g.fleet);
    $("gh-fleet").textContent = g.fleet || "";
    show("gh-fleet-error", !!g.fleetError);
    $("gh-fleet-error").textContent = g.fleetError || "";
    show("gh-share-row", !!g.canShare);
    setInput("gh-share", !!g.shareWithFleet);
    setInput("gh-label", g.label);
    if (![...$("gh-every").options].some((o) => +o.value === g.publishEveryMinutes)) {
      $("gh-every").append(new Option(g.publishEveryMinutes + " minutes", g.publishEveryMinutes));
    }
    setInput("gh-every", String(g.publishEveryMinutes));
    setInput("gh-country", !!g.showCountry);
    // An adopted sign-in follows its sharer's choice until one is made here.
    $("gh-country-from").textContent = g.showCountryFrom ? "(from " + g.showCountryFrom + ")" : "";
    show("gh-unlock-copy", !!g.canUnlock);
  }

  // Server.
  const v = s.server, c = s.connect;
  const svBadge = $("sv-badge");
  svBadge.className = "badge" + (v.enrolled ? (v.lastError ? " err" : " on") : "");
  svBadge.textContent = connecting ? "connecting…" : v.enrolled ? (v.lastError ? "error" : "on") : "off";
  show("sv-info", v.enrolled);
  $("sv-url").textContent = v.url || "";
  $("sv-id").textContent = v.machineId || "";
  $("sv-last").textContent = ago(v.lastUpload);
  const err = (c && c.error) || v.lastError || "";
  show("sv-error", !!err);
  $("sv-error").textContent = err;
  setInput("sv-input", v.url || v.default || "");
  show("sv-connecting", connecting);
  $("sv-connect").disabled = connecting;
  $("sv-connect").textContent = v.enrolled ? "Change server" : "Connect";
  show("sv-off", v.enrolled || !!v.url);

  // This app: tray only, or with its window (a change restarts it).
  const app = s.app || {};
  show("app", !!app.canSwitch);
  $("app-tray-text").textContent = "Run only in the " + (app.tray || "system tray");
  setInput("app-tray", !!app.trayOnly);

  schedule(signing || connecting ? 1000 : 10000);
}

function schedule(ms) {
  clearTimeout(timer);
  if (!gone) timer = setTimeout(refresh, ms);
}

async function refresh() {
  if (gone) return;
  try {
    state = await api("state");
    misses = 0;
    render();
  } catch (e) {
    // Twice in a row: the app is gone (a single miss may be a hiccup).
    if (e instanceof Offline && ++misses >= 2) return goneNow();
    schedule(2000);
  }
}

function busy(btn, fn) {
  return async () => {
    if (gone) return;
    btn.disabled = true;
    try {
      await fn();
    } catch (e) {
      if (e instanceof Offline) return goneNow();
      alertIn(btn, e.message);
    } finally {
      if (!gone) btn.disabled = false;
      await refresh();
    }
  };
}

// alertIn shows msg under btn's row for a few seconds.
function alertIn(btn, msg, kind = "error") {
  const row = btn.closest(".row, div");
  let p = row.nextElementSibling;
  if (!p || !p.classList.contains("inline")) {
    p = document.createElement("p");
    row.after(p);
  }
  p.className = kind + " inline";
  p.textContent = msg;
  clearTimeout(p.timer);
  p.timer = setTimeout(() => p.remove(), 8000);
}

for (const id of ["gh-new-label", "gh-label", "gh-every", "gh-country", "gh-share", "sv-input", "sv-join"]) {
  $(id).addEventListener("input", () => dirty.add(id));
  $(id).addEventListener("change", () => dirty.add(id));
}

// #github opens Advanced at the GitHub sign-in, #advanced at its top (the
// app's Connect GitHub… and Advanced…).
function reveal(section) {
  $("advanced").open = true;
  const el = $(section === "github" ? "gh" : "advanced");
  el.scrollIntoView({ block: "start", behavior: "smooth" });
  if (section === "github" && !$("gh-off").hidden) $("gh-new-label").focus({ preventScroll: true });
}
const revealHash = () => {
  const h = location.hash.slice(1);
  if (h === "github" || h === "advanced") reveal(h);
};
window.addEventListener("hashchange", revealHash);
revealHash();
$("gh-connect").addEventListener("click", () => reveal("github"));

$("gh-signin").addEventListener("click", busy($("gh-signin"), async () => {
  await api("github/login", { label: $("gh-new-label").value });
  dirty.delete("gh-new-label");
}));
$("gh-cancel").addEventListener("click", busy($("gh-cancel"), () => api("github/cancel", {})));
$("gh-copy").addEventListener("click", () => navigator.clipboard?.writeText($("gh-code").textContent));
$("gh-save").addEventListener("click", busy($("gh-save"), async () => {
  const opts = { label: $("gh-label").value, publishEveryMinutes: +$("gh-every").value, showCountry: $("gh-country").checked };
  if (state && state.github.canShare) opts.shareWithFleet = $("gh-share").checked;
  await api("github/options", opts);
  ["gh-label", "gh-every", "gh-country", "gh-share"].forEach((id) => dirty.delete(id));
  alertIn($("gh-save"), "Saved.", "hint");
}));
$("gh-publish").addEventListener("click", busy($("gh-publish"), () => api("sync", {})));

// Open dashboard signs the owner in (pages/dashboard/owner.ts, "Owner
// unlock"): the dashboard opened at #unlock says it is ready; only then is
// the owner key fetched, and it is posted to that window alone, at the
// dashboard's exact origin. It never goes into an address, unless the owner
// copies the sign-in link. Without a key to hand over, it opens the
// dashboard as anyone sees it.
let unlockWin = null;
const dashboard = () => {
  const u = state && state.github.on ? state.github.pagesUrl || "" : "";
  try {
    return { url: u, base: u.split("#")[0], origin: new URL(u).origin, signIn: !!state.github.canUnlock };
  } catch {
    return null;
  }
};
$("gh-dashboard").addEventListener("click", () => {
  const d = dashboard();
  if (!d) return;
  if (d.signIn) unlockWin = window.open(d.base + "#unlock", "_blank"); // keeps window.opener for the handoff
  else window.open(d.url, "_blank", "noopener");
});
window.addEventListener("message", async (e) => {
  const d = dashboard();
  if (!d || !d.signIn || !unlockWin || e.source !== unlockWin || e.origin !== d.origin || !e.data || e.data.type !== "tokenmaxr-unlock-ready") return;
  const win = unlockWin;
  try {
    const r = await api("github/unlock", {});
    win.postMessage({ type: "tokenmaxr-unlock", key: r.key }, d.origin);
  } catch (err) {
    if (err instanceof Offline) return goneNow();
    alertIn($("gh-dashboard"), err.message);
  }
});
$("gh-unlock-copy").addEventListener("click", busy($("gh-unlock-copy"), async () => {
  const d = dashboard();
  if (!d) return;
  const link = api("github/unlock", {}).then((r) => d.base + "#unlock=" + r.key);
  if (window.ClipboardItem) {
    // Safari copies only within the click: the write starts now, with the
    // link to come.
    const write = navigator.clipboard.write([new ClipboardItem({ "text/plain": link.then((t) => new Blob([t], { type: "text/plain" })) })]);
    write.catch(() => {});
    await link; // the server's error, if any, rather than the clipboard's
    await write;
  } else {
    await navigator.clipboard.writeText(await link);
  }
  alertIn($("gh-unlock-copy"), "Copied. Anyone with the link sees your dashboard signed in.", "hint");
}));
$("gh-signout").addEventListener("click", busy($("gh-signout"), async () => {
  const g = state ? state.github : {};
  let msg = "Stop publishing this machine to GitHub? What it published stays.";
  if (g.adopted) msg = "Stop publishing this machine to GitHub? It stops using your fleet's shared sign-in. What it published stays.";
  else if (g.canShare && g.shareWithFleet) msg += " Your other machines stop too (they use this sign-in).";
  if (!confirm(msg)) return;
  await api("github/logout", {});
}));
$("sv-connect").addEventListener("click", busy($("sv-connect"), async () => {
  await api("server", { server: $("sv-input").value, join: $("sv-join").value });
  dirty.delete("sv-input"); dirty.delete("sv-join");
  $("sv-join").value = "";
}));
$("sv-cancel").addEventListener("click", busy($("sv-cancel"), () => api("server/cancel", {})));
$("app-tray").addEventListener("change", async () => {
  const box = $("app-tray");
  box.disabled = true;
  try {
    const r = await api("app", { trayOnly: box.checked });
    if (r.restart) return goneNow(); // the page goes with the app
  } catch (e) {
    if (e instanceof Offline) return goneNow();
    box.checked = !box.checked;
    show("app-error", true);
    $("app-error").textContent = e.message;
    setTimeout(() => show("app-error", false), 8000);
  }
  box.disabled = false;
  await refresh();
});
$("sv-off").addEventListener("click", busy($("sv-off"), async () => {
  if (!confirm("Stop pushing to the server? This machine stays enrolled; connect again to resume.")) return;
  await api("server", { server: "off" });
  dirty.delete("sv-input");
}));

refresh();
