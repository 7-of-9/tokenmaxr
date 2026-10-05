// Collector settings page: polls api/state and posts changes. Everything is
// same-origin JSON; the page never sees a token or key, apart from the
// dashboard's owner key, fetched only to unlock the dashboard (below).
"use strict";

const $ = (id) => document.getElementById(id);
const show = (id, on) => { $(id).hidden = !on; };
let state = null;
let timer = 0;
let restarting = false; // the app is restarting in its other mode: this page is done
const dirty = new Set(); // inputs the user is editing: polling leaves them alone

async function api(path, body) {
  const opts = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  const r = await fetch("api/" + path, opts);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.status + " " + r.statusText);
  return j;
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
  if (!s) return;
  $("who").textContent = (s.machine || "This machine") + (s.fleet ? " · fleet " + s.fleet : "") + (s.setUp ? "" : " · not set up");
  $("foot").textContent = "Collector " + (s.version === "dev" ? "dev build" : "v" + s.version) + (s.buildTime ? " · built " + s.buildTime.replace("T", " ").replace(/:\d\dZ$/, " UTC") : "");

  // GitHub.
  const g = s.github, l = s.login;
  const signing = !!(l && l.running);
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
    link($("gh-code-link"), l.codeUrl || "https://github.com/login/device", "Open the GitHub sign-in page");
    show("gh-step-box", !!l.step && signing);
    $("gh-step").textContent = l.step || "";
    link($("gh-step-link"), l.stepUrl || "", "Open it on GitHub");
    const ul = $("gh-progress");
    ul.replaceChildren(...(l.progress || []).map((p) => Object.assign(document.createElement("li"), { textContent: p })));
    show("gh-login-error", !!l.error);
    $("gh-login-error").textContent = l.error || "";
    $("gh-cancel").textContent = signing ? "Cancel" : "Back";
  }
  if (g.on) {
    link($("gh-repo"), "https://github.com/" + g.repo, g.repo);
    $("gh-who").textContent = g.adopted ? "signed in through your fleet (shared by " + g.login + ")" : "signed in as " + g.login;
    const pages = $("gh-pages");
    if (g.pagesUrl) {
      const a = document.createElement("a");
      a.target = "_blank"; a.rel = "noopener";
      link(a, g.pagesUrl);
      pages.replaceChildren(a);
    } else {
      pages.textContent = "not yet (the first build takes a few minutes)";
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
    setInput("gh-quota", !g.noQuota);
    setInput("gh-country", !!g.showCountry);
    setInput("gh-history", !!g.showAccountHistory);
    // An adopted sign-in follows its sharer's choices until one is made here.
    $("gh-country-from").textContent = g.showCountryFrom ? "(from " + g.showCountryFrom + ")" : "";
    $("gh-history-from").textContent = g.showAccountHistoryFrom ? "(from " + g.showAccountHistoryFrom + ")" : "";
    show("gh-unlock-box", !!g.canUnlock);
  }

  // Server.
  const v = s.server, c = s.connect;
  const connecting = !!(c && c.running);
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
  setInput("app-tray", !!app.trayOnly);

  schedule(signing || connecting ? 1000 : 10000);
}

function schedule(ms) {
  clearTimeout(timer);
  timer = setTimeout(refresh, ms);
}

async function refresh() {
  if (restarting) return;
  try {
    state = await api("state");
    render();
  } catch (e) {
    $("who").textContent = "The collector app is not reachable (it may have quit or restarted). Open Settings from its icon again.";
    schedule(5000);
  }
}

function busy(btn, fn) {
  return async () => {
    btn.disabled = true;
    try {
      await fn();
    } catch (e) {
      alertIn(btn, e.message);
    } finally {
      btn.disabled = false;
      await refresh();
    }
  };
}

function alertIn(btn, msg, kind = "error") {
  const card = btn.closest(".card");
  let p = card.querySelector(".inline");
  if (!p) {
    p = document.createElement("p");
    btn.closest(".row, div").after(p);
  }
  p.className = kind + " inline";
  p.textContent = msg;
  setTimeout(() => p.remove(), 8000);
}

for (const id of ["gh-new-label", "gh-label", "gh-every", "gh-quota", "gh-country", "gh-history", "gh-share", "sv-input", "sv-join"]) {
  $(id).addEventListener("input", () => dirty.add(id));
  $(id).addEventListener("change", () => dirty.add(id));
}

$("gh-signin").addEventListener("click", busy($("gh-signin"), async () => {
  await api("github/login", { label: $("gh-new-label").value });
  dirty.delete("gh-new-label");
}));
$("gh-cancel").addEventListener("click", busy($("gh-cancel"), () => api("github/cancel", {})));
$("gh-copy").addEventListener("click", () => navigator.clipboard?.writeText($("gh-code").textContent));
$("gh-save").addEventListener("click", busy($("gh-save"), async () => {
  const opts = {
    label: $("gh-label").value, publishEveryMinutes: +$("gh-every").value,
    noQuota: !$("gh-quota").checked, showCountry: $("gh-country").checked,
    showAccountHistory: $("gh-history").checked,
  };
  if (state && state.github.canShare) opts.shareWithFleet = $("gh-share").checked;
  await api("github/options", opts);
  ["gh-label", "gh-every", "gh-quota", "gh-country", "gh-history", "gh-share"].forEach((id) => dirty.delete(id));
}));
$("gh-publish").addEventListener("click", busy($("gh-publish"), () => api("sync", {})));

// Owner unlock (pages/dashboard/owner.ts): the dashboard opened at #unlock
// says it is ready; only then is the owner key fetched, and it is posted to
// that window alone, at the dashboard's exact origin. It never goes into an
// address, unless the owner copies the unlock link.
let unlockWin = null;
const dashboard = () => {
  const u = state && state.github.canUnlock ? state.github.pagesUrl : "";
  try {
    return { base: u.split("#")[0], origin: new URL(u).origin };
  } catch {
    return null;
  }
};
$("gh-unlock").addEventListener("click", () => {
  const d = dashboard();
  if (d) unlockWin = window.open(d.base + "#unlock", "_blank"); // keeps window.opener for the handoff
});
window.addEventListener("message", async (e) => {
  const d = dashboard();
  if (!d || !unlockWin || e.source !== unlockWin || e.origin !== d.origin || !e.data || e.data.type !== "tokenmaxr-unlock-ready") return;
  const win = unlockWin;
  try {
    const r = await api("github/unlock", {});
    win.postMessage({ type: "tokenmaxr-unlock", key: r.key }, d.origin);
  } catch (err) {
    alertIn($("gh-unlock"), err.message);
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
  alertIn($("gh-unlock-copy"), "Copied. Treat the link like a password.", "hint");
}));
$("gh-signout").addEventListener("click", busy($("gh-signout"), async () => {
  const g = state ? state.github : {};
  const msg = g.adopted
    ? "Stop publishing this machine to GitHub? It no longer takes the sign-in your fleet shares (sign in here to publish again). What it published stays in the repository."
    : "Stop publishing this machine to GitHub? What it published stays in the repository." + (g.canShare && g.shareWithFleet ? " The sign-in it shares with your other machines is withdrawn, so they stop publishing within the hour (to make the sign-in itself useless, revoke the App at github.com/settings/installations, which stops every machine)." : "");
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
    if (r.restart) {
      restarting = true;
      clearTimeout(timer);
      show("app-restart", true);
      return;
    }
  } catch (e) {
    box.checked = !box.checked;
    show("app-error", true);
    $("app-error").textContent = e.message;
    setTimeout(() => show("app-error", false), 8000);
  }
  box.disabled = false;
  await refresh();
});
$("sv-off").addEventListener("click", busy($("sv-off"), async () => {
  if (!confirm("Stop uploading to the server? This machine stays enrolled; connect again to resume.")) return;
  await api("server", { server: "off" });
  dirty.delete("sv-input");
}));

refresh();
