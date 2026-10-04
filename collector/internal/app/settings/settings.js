// Collector settings page: polls api/state and posts changes. Everything is
// same-origin JSON; the page never sees a token or key.
"use strict";

const $ = (id) => document.getElementById(id);
const show = (id, on) => { $(id).hidden = !on; };
let state = null;
let timer = 0;
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
    $("gh-user").textContent = g.login;
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
    setInput("gh-label", g.label);
    if (![...$("gh-every").options].some((o) => +o.value === g.publishEveryMinutes)) {
      $("gh-every").append(new Option(g.publishEveryMinutes + " minutes", g.publishEveryMinutes));
    }
    setInput("gh-every", String(g.publishEveryMinutes));
    setInput("gh-quota", !g.noQuota);
    setInput("gh-country", !!g.showCountry);
    setInput("gh-history", !!g.showAccountHistory);
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

  schedule(signing || connecting ? 1000 : 10000);
}

function schedule(ms) {
  clearTimeout(timer);
  timer = setTimeout(refresh, ms);
}

async function refresh() {
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

function alertIn(btn, msg) {
  const card = btn.closest(".card");
  let p = card.querySelector(".error.inline");
  if (!p) {
    p = Object.assign(document.createElement("p"), { className: "error inline" });
    btn.closest(".row, div").after(p);
  }
  p.textContent = msg;
  setTimeout(() => p.remove(), 8000);
}

for (const id of ["gh-new-label", "gh-label", "gh-every", "gh-quota", "gh-country", "gh-history", "sv-input", "sv-join"]) {
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
  await api("github/options", {
    label: $("gh-label").value, publishEveryMinutes: +$("gh-every").value,
    noQuota: !$("gh-quota").checked, showCountry: $("gh-country").checked,
    showAccountHistory: $("gh-history").checked,
  });
  ["gh-label", "gh-every", "gh-quota", "gh-country", "gh-history"].forEach((id) => dirty.delete(id));
}));
$("gh-publish").addEventListener("click", busy($("gh-publish"), () => api("sync", {})));
$("gh-signout").addEventListener("click", busy($("gh-signout"), async () => {
  if (!confirm("Stop publishing this machine to GitHub? What it published stays in the repository.")) return;
  await api("github/logout", {});
}));
$("sv-connect").addEventListener("click", busy($("sv-connect"), async () => {
  await api("server", { server: $("sv-input").value, join: $("sv-join").value });
  dirty.delete("sv-input"); dirty.delete("sv-join");
  $("sv-join").value = "";
}));
$("sv-cancel").addEventListener("click", busy($("sv-cancel"), () => api("server/cancel", {})));
$("sv-off").addEventListener("click", busy($("sv-off"), async () => {
  if (!confirm("Stop uploading to the server? This machine stays enrolled; connect again to resume.")) return;
  await api("server", { server: "off" });
  dirty.delete("sv-input");
}));

refresh();
