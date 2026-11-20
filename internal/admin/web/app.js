// Spikes402 admin dashboard. Plain JS, no dependencies, no build step.
// Everything from the server is inserted with textContent, never as HTML:
// paths and bot names come from visitors and must not be trusted.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { key: sessionStorage.getItem("spikes402-key") || "", seq: 0, first: true, recent: [], sprites: [], settingsLoaded: false, timers: [] };

const CLASS_COLOR = { "verified-signed": "#5b8cff", verified: "#3ddc84", impostor: "#ff4d6d", unknown: "#9aa3c7" };
const OUTCOME_LABEL = { paid: "paid ⚡", "pow-solved": "did the work ⚙", free: "let in (policy)", challenged: "held at the wall (402)",
  rejected: "bad proof, repelled", "rate-limited": "too fast, repelled (429)", busy: "wait your turn (503)", blocked: "blocked (403)" };

async function api(method, path, body) {
  const res = await fetch(path, {
    method, headers: { Authorization: "Bearer " + state.key, ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined, cache: "no-store",
  });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) { logout("Wrong admin key."); throw new Error("unauthorized"); }
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined && text !== null) e.textContent = String(text);
  if (cls) e.className = cls;
  return e;
}
function kv(table, rows) {
  table.replaceChildren(...rows.map(([k, v, cls]) => { const tr = el("tr"); tr.append(el("td", k)); tr.append(el("td", v, "num " + (cls || ""))); return tr; }));
}
const fmt = (n) => (n ?? 0).toLocaleString();
const time = (t) => new Date(t).toLocaleTimeString();

// --- login and tabs ---
async function login() {
  try {
    const me = await api("GET", "/api/whoami");
    sessionStorage.setItem("spikes402-key", state.key);
    $("login").hidden = true; $("tabs").hidden = false; $("logout").hidden = false;
    $("withdraw-tab").hidden = !me.withdraw_enabled;
    if (me.role !== "admin") $("login-error").textContent = "This key has the AI role: read and propose only.";
    show("siege");
    state.timers.push(setInterval(pollSiege, 1000), setInterval(refresh, 3000));
    pollSiege(); refresh();
    requestAnimationFrame(frame);
  } catch (e) { $("login-error").textContent = e.message; }
}
function logout(msg) {
  sessionStorage.removeItem("spikes402-key"); state.key = "";
  state.timers.forEach(clearInterval); state.timers = [];
  document.querySelectorAll(".tab").forEach((s) => (s.hidden = true));
  $("tabs").hidden = true; $("logout").hidden = true; $("login").hidden = false;
  $("login-error").textContent = msg || "";
}
function show(tab) {
  document.querySelectorAll(".tab").forEach((s) => (s.hidden = s.id !== tab));
  document.querySelectorAll("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
}
$("login-form").addEventListener("submit", (e) => { e.preventDefault(); state.key = $("key").value.trim(); $("key").value = ""; login(); });
$("logout").addEventListener("click", () => logout());
$("tabs").addEventListener("click", (e) => { const t = e.target.closest("button"); if (t) show(t.dataset.tab); });

// --- siege monitor ---
async function pollSiege() {
  let data;
  try { data = await api("GET", "/api/siege?since=" + state.seq); } catch { return; }
  if (data.next < state.seq) state.seq = 0; // server restarted
  const visits = data.visits || [];
  state.seq = Math.max(state.seq, data.next);
  const now = Date.now();
  for (const v of visits) state.recent.push(Date.parse(v.time));
  state.recent = state.recent.filter((t) => now - t < 60000);
  $("s-rate").textContent = fmt(state.recent.length);
  const c = data.counts, o = c.by_outcome || {};
  $("s-repelled").textContent = fmt((o.rejected || 0) + (o["rate-limited"] || 0) + (o.blocked || 0) + (o.busy || 0));
  $("s-paid").textContent = fmt(o.paid); $("s-pow").textContent = fmt(o["pow-solved"]);
  $("s-impostor").textContent = fmt((c.by_class || {}).impostor);
  renderLegend(c); renderBots(c); renderFeed(visits);
  if (!state.first && $("animate").checked && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
    const show = visits.slice(-40);
    show.forEach((v, i) => setTimeout(() => spawn(v), (i * 900) / show.length));
  }
  state.first = false;
}

function renderLegend(c) {
  const cls = c.by_class || {};
  $("legend").replaceChildren(...Object.keys(CLASS_COLOR).map((k) => {
    const s = el("span"); const dot = el("i"); dot.style.background = CLASS_COLOR[k];
    s.append(dot, k + " " + fmt(cls[k])); return s;
  }), el("span", "· challenged " + fmt((c.by_outcome || {}).challenged) + " · total " + fmt(c.total)));
}

function renderBots(c) {
  const rows = Object.entries(c.by_bot || {}).sort((a, b) => b[1] - a[1]).slice(0, 15);
  if (!rows.length) { $("bots").replaceChildren(el("tr", null)); $("bots").firstChild.append(el("td", "No known crawlers yet.")); return; }
  kv($("bots"), rows.map(([k, v]) => [k, fmt(v), k.startsWith("fake ") ? "bad-text" : ""]));
}

function renderFeed(visits) {
  const feed = $("feed");
  for (const v of visits) {
    const tr = el("tr");
    tr.append(el("td", time(v.time)), el("td", v.visitor));
    const c = el("td"); c.append(el("span", v.class, "pill c-" + v.class)); tr.append(c);
    tr.append(el("td", v.bot || ""), el("td", v.path), el("td", OUTCOME_LABEL[v.outcome] || v.outcome, "o-" + v.outcome));
    feed.prepend(tr);
  }
  while (feed.rows.length > 40) feed.deleteRow(feed.rows.length - 1);
}

// Sprites: a visitor walks from the frontier to the energy wall; then,
// depending on the outcome, passes the gate, waits, bounces or bursts.
const SVGNS = "http://www.w3.org/2000/svg";
const WALL_X = 610, GATE_Y = 220;
const PASS = { paid: ["⚡", "#ffc83d"], "pow-solved": ["⚙", "#00e5ff"], free: ["✓", "#3ddc84"] };
const STOP = { challenged: ["402", "#ffe08a", 0], rejected: ["✗", "#ff4d6d", 130], "rate-limited": ["429", "#ff9f43", 230],
  busy: ["503", "#9aa3c7", 60], blocked: ["403", "#ff4d6d", 0] };

function spawn(v) {
  if (state.sprites.length > 150) state.sprites.shift().g.remove();
  const g = document.createElementNS(SVGNS, "g"); g.setAttribute("class", "sprite");
  const c = document.createElementNS(SVGNS, "circle"); c.setAttribute("r", 7); c.setAttribute("fill", CLASS_COLOR[v.class] || "#999");
  if (v.class === "verified-signed") { c.setAttribute("stroke", "#fff"); c.setAttribute("stroke-width", 2); }
  g.append(c);
  if (v.bot) { const t = document.createElementNS(SVGNS, "text"); t.setAttribute("x", 10); t.setAttribute("y", 4); t.textContent = v.bot; g.append(t); }
  const tag = document.createElementNS(SVGNS, "text"); tag.setAttribute("class", "tag"); tag.setAttribute("x", -8); tag.setAttribute("y", -11); g.append(tag);
  $("sprites").append(g);
  const y0 = 50 + Math.random() * 330;
  const passes = v.outcome in PASS;
  state.sprites.push({ g, c, tag, v, t0: performance.now(), x0: 20 + Math.random() * 60, y0, ty: passes ? GATE_Y + (Math.random() * 30 - 15) : y0 });
}

const ease = (t) => (t < 0 ? 0 : t > 1 ? 1 : 1 - Math.pow(1 - t, 3));
function frame(now) {
  state.sprites = state.sprites.filter((s) => {
    const t = now - s.t0, walk = 1600, after = t - walk;
    let x, y, op = 1, r = 7;
    if (after < 0) { const k = ease(t / walk); x = s.x0 + (WALL_X - s.x0) * k; y = s.y0 + (s.ty - s.y0) * k; }
    else if (s.v.outcome in PASS) {
      const [sym, col] = PASS[s.v.outcome]; const k = ease(after / 1400);
      x = WALL_X + (860 - WALL_X) * k; y = s.ty + (245 - s.ty) * k; op = 1 - Math.max(0, (after - 900) / 600);
      s.tag.textContent = sym; s.tag.setAttribute("fill", col);
    } else {
      const [sym, col, back] = STOP[s.v.outcome] || ["?", "#999", 0]; const k = ease(after / 900);
      x = WALL_X - back * k; y = s.ty; op = 1 - Math.max(0, (after - 700) / 1300);
      if (s.v.outcome === "blocked") r = 7 + 14 * k;
      s.tag.textContent = sym; s.tag.setAttribute("fill", col);
      if (after < 80 && !s.sparked) { s.sparked = true; spark(WALL_X + 12, y); }
    }
    s.c.setAttribute("r", r);
    s.g.setAttribute("transform", `translate(${x.toFixed(1)},${y.toFixed(1)})`);
    s.g.setAttribute("opacity", Math.max(0, op).toFixed(2));
    if (op <= 0) { s.g.remove(); return false; }
    return true;
  });
  if (state.key) requestAnimationFrame(frame);
}
function spark(x, y) {
  const c = document.createElementNS(SVGNS, "circle");
  c.setAttribute("class", "spark"); c.setAttribute("cx", x); c.setAttribute("cy", y); c.setAttribute("r", 4);
  $("sprites").append(c);
  const t0 = performance.now();
  (function grow(now) { const k = (now - t0) / 500; c.setAttribute("r", 4 + 16 * k); c.setAttribute("opacity", 1 - k); if (k < 1) requestAnimationFrame(grow); else c.remove(); })(t0);
}

// --- overview, settings, proposals, withdraw ---
async function refresh() {
  let st;
  try { st = await api("GET", "/api/status"); } catch { return; }
  const o = st.counts.by_outcome || {}, g = st.gate, n = g.node;
  kv($("ov-traffic"), [["Up for", Math.round(st.uptime_seconds / 60) + " min"], ["Requests to protected pages", fmt(st.counts.total)],
    ...Object.keys(OUTCOME_LABEL).map((k) => [OUTCOME_LABEL[k], fmt(o[k]), "o-" + k])]);
  kv($("ov-classes"), Object.keys(CLASS_COLOR).map((k) => [k, fmt((st.counts.by_class || {})[k])]));
  const node = [["Backend", st.settings.lightning_backend],
    ["Circuit breaker", n.breaker_open ? "OPEN until " + time(n.breaker_open_until) : "closed (healthy)", n.breaker_open ? "bad-text" : "ok-text"],
    ["Invoices created", fmt(n.invoices_created)], ["Invoice failures", fmt(n.invoice_failures)],
    ["Invoice budget now", n.invoice_budget.toFixed(1)], ["Calls in flight", n.in_flight]];
  if (n.last_error) node.push(["Last error", n.last_error + " (" + time(n.last_error_at) + ")", "bad-text"]);
  if (st.node) node.push(["Balance", fmt(st.node.balance_sat) + " sats"], ["Channels", st.node.channels], ["Node", st.node.node_id.slice(0, 20)]);
  if (st.node_error) node.push(["Node status", st.node_error, "bad-text"]);
  kv($("ov-node"), node);
  kv($("ov-gate"), [["Open invoices", fmt(g.open_invoices)], ["Used tokens remembered", fmt(g.spent_tokens)],
    ["Pending proposals", fmt(st.pending_proposals)]]);
  const s = st.settings;
  kv($("set-current"), [["Origin", s.origin], ["Protected", s.protect.join(" ")], ["Price", s.price_sats + " sats"],
    ["Proof-of-work", `${s.current_pow.Algorithm} difficulty ${s.current_pow.Difficulty}` + (s.current_pow.MemoryMiB ? `, ${s.current_pow.MemoryMiB} MiB` : "")],
    ["PoW seconds per sat", s.pow_seconds_per_sat], ["402 answers / client / min", s.challenges_per_minute],
    ["Invoices / min (all)", s.max_invoices_per_minute], ["Bot identification", s.identify_bots ? "on" : "off"],
    ["Bot policies", Object.keys(s.bot_policies).length ? JSON.stringify(s.bot_policies) : "none (everyone pays the same)"],
    ["Visitor IPs in monitor", s.visitor_ips], ["AI may apply changes", s.ai_can_apply ? "yes" : "no (propose only)"],
    ["Withdrawals", s.withdraw_enabled ? "enabled → " + s.withdraw_destination : "disabled"]]);
  if (!state.settingsLoaded) fillSettings(s);
  $("prop-badge").hidden = !st.pending_proposals; $("prop-badge").textContent = st.pending_proposals;
  if (st.node) $("wd-balance").textContent = fmt(st.node.balance_sat);
  $("wd-dest").textContent = s.withdraw_destination || "";
  loadProposals();
  if (!$("withdraw-tab").hidden) loadWithdrawals();
}

async function loadWithdrawals() {
  let w;
  try { w = await api("GET", "/api/withdraw"); } catch { return; }
  const rows = (w.history || []).slice().reverse().map((h) => [time(h.time) + " · " + fmt(h.amount_sat) + " sats", (h.ok ? "✓ " : "✗ ") + h.result, h.ok ? "ok-text" : "bad-text"]);
  if (w.locked_until) rows.unshift(["Locked", "until " + time(w.locked_until), "bad-text"]);
  kv($("wd-history"), rows.length ? rows : [["none yet", ""]]);
}

function fillSettings(s) {
  $("f-price").value = s.price_sats; $("f-sps").value = s.pow_seconds_per_sat; $("f-cpm").value = s.challenges_per_minute;
  $("f-pol").value = JSON.stringify(s.bot_policies, null, 2); state.settingsLoaded = true; state.current = s;
}

$("set-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const msg = $("set-msg"), cur = state.current, ch = {};
  try {
    if (+$("f-price").value !== cur.price_sats) ch.price_sats = +$("f-price").value;
    if (+$("f-sps").value !== cur.pow_seconds_per_sat) ch.pow_seconds_per_sat = +$("f-sps").value;
    if (+$("f-cpm").value !== cur.challenges_per_minute) ch.challenges_per_minute = +$("f-cpm").value;
    const pol = JSON.parse($("f-pol").value || "{}");
    if (JSON.stringify(pol) !== JSON.stringify(cur.bot_policies)) ch.bot_policies = pol;
    if (!Object.keys(ch).length) { msg.className = "msg"; msg.textContent = "Nothing changed."; return; }
    const s = await api("PUT", "/api/settings", ch);
    fillSettings(s); msg.className = "msg ok"; msg.textContent = "Applied (until restart).";
    refresh();
  } catch (err) { msg.className = "msg err"; msg.textContent = err.message; }
});

async function loadProposals() {
  let list;
  try { list = await api("GET", "/api/proposals"); } catch { return; }
  const box = $("prop-list");
  if (!list.length) { box.replaceChildren(el("p", "No proposals.", "note")); return; }
  box.replaceChildren(...list.map((p) => {
    const d = el("div", null, "card");
    d.append(el("p", `${time(p.created)} · by ${p.by} · ${p.status}` + (p.result ? " · " + p.result : "")));
    if (p.reason) d.append(el("p", "Reason: " + p.reason));
    d.append(el("pre", JSON.stringify(p.changes, null, 2)));
    if (p.status === "pending") {
      for (const [act, label] of [["approve", "Approve and apply"], ["reject", "Reject"]]) {
        const b = el("button", label, act === "reject" ? "" : "danger");
        b.addEventListener("click", async () => {
          try { await api("POST", `/api/proposals/${encodeURIComponent(p.id)}/${act}`); state.settingsLoaded = false; refresh(); }
          catch (err) { alert(err.message); }
        });
        d.append(b, " ");
      }
    }
    return d;
  }));
}

let prepared = null;
$("wd-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const msg = $("wd-msg"); msg.className = "msg"; msg.textContent = "";
  try {
    prepared = await api("POST", "/api/withdraw/prepare", { amount_sat: +$("wd-amount").value, password: $("wd-pw").value });
    $("wd-pw").value = "";
    $("wd-c-amount").textContent = fmt(prepared.amount_sat); $("wd-c-dest").textContent = prepared.destination;
    $("wd-c-exp").textContent = time(prepared.expires_at);
    $("wd-form").hidden = true; $("wd-confirm").hidden = false;
  } catch (err) { msg.className = "msg err"; msg.textContent = err.message; }
});
$("wd-cancel").addEventListener("click", () => { prepared = null; $("wd-confirm").hidden = true; $("wd-form").hidden = false; });
$("wd-confirm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const msg = $("wd-msg"), amount = +$("wd-amount2").value;
  if (!prepared || amount !== prepared.amount_sat) { msg.className = "msg err"; msg.textContent = "The amount does not match."; return; }
  if (!confirm(`Last check: send ${fmt(amount)} sats to ${prepared.destination}? This cannot be undone.`)) return;
  try {
    const r = await api("POST", "/api/withdraw/confirm", { confirm_id: prepared.confirm_id, amount_sat: amount, password: $("wd-pw2").value });
    msg.className = "msg ok"; msg.textContent = "Sent: " + r.result;
  } catch (err) { msg.className = "msg err"; msg.textContent = err.message; }
  $("wd-pw2").value = ""; $("wd-amount2").value = ""; prepared = null;
  $("wd-confirm").hidden = true; $("wd-form").hidden = false; refresh();
});

if (state.key) login();
