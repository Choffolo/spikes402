// Spikes402 admin dashboard. Plain JS, no dependencies, no build step.
// Everything from the server is inserted with textContent, never as HTML:
// paths and bot names come from visitors and must not be trusted.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { key: sessionStorage.getItem("spikes402-key") || "", seq: 0, first: true, recent: [], sprites: [], timers: [] };

// One colour per outcome group: the same four as the numbers on top.
const GROUP = { paid: "paid", "pow-solved": "pow", free: "pow", challenged: "held",
  rejected: "repelled", "rate-limited": "repelled", busy: "repelled", blocked: "repelled" };
const COLOR = { paid: "#ffc83d", pow: "#00e5ff", held: "#9aa3c7", repelled: "#ff4d6d" };
const OUTCOME = { paid: "paid ⚡", "pow-solved": "did the work", free: "let in (policy)", challenged: "stopped (402)",
  rejected: "fake proof", "rate-limited": "too fast (429)", busy: "busy (503)", blocked: "blocked (403)" };

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
function rows(table, list) {
  table.replaceChildren(...list.map((cells) => { const tr = el("tr"); cells.forEach((c) => tr.append(el("td", c))); return tr; }));
}
const fmt = (n) => (n ?? 0).toLocaleString();
const time = (t) => new Date(t).toLocaleTimeString();
const who = (v) => (v.bot || "unknown") + (v.class === "impostor" ? " (fake)" : "");
const msg = (id, text, cls) => { $(id).className = "msg " + (cls || ""); $(id).textContent = text; };

// --- login and pages ---
async function login() {
  try {
    const me = await api("GET", "/api/whoami");
    sessionStorage.setItem("spikes402-key", state.key);
    $("login").hidden = true; $("nav").hidden = false; $("node").hidden = false;
    $("withdraw").hidden = !me.withdraw_enabled;
    state.role = me.role;
    if (me.role !== "admin") $("login-error").textContent = "This key has the AI role: it can suggest changes; it may apply only those that do not reduce protection.";
    route();
    state.timers.push(setInterval(pollSiege, 1000), setInterval(refresh, 5000));
    pollSiege(); refresh();
    requestAnimationFrame(frame);
  } catch (e) { $("login-error").textContent = e.message; }
}
function logout(text) {
  sessionStorage.removeItem("spikes402-key"); state.key = "";
  state.timers.forEach(clearInterval); state.timers = [];
  document.querySelectorAll(".page").forEach((s) => (s.hidden = true));
  $("nav").hidden = true; $("node").hidden = true; $("login").hidden = false;
  $("pause-banner").hidden = true; document.body.classList.remove("paused");
  $("login-error").textContent = text || "";
}
function route() {
  if (!state.key) return;
  const settings = location.hash === "#settings";
  $("siege").hidden = settings; $("settings").hidden = !settings; $("nav").hidden = settings;
  if (settings) { loadSettings(); loadProposals(); loadWithdrawals(); }
}
window.addEventListener("hashchange", route);
$("login-form").addEventListener("submit", (e) => { e.preventDefault(); state.key = $("key").value.trim(); $("key").value = ""; login(); });
$("logout").addEventListener("click", () => { location.hash = ""; logout(); });

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
  const o = data.counts.by_outcome || {};
  $("s-rate").textContent = fmt(state.recent.length);
  $("s-paid").textContent = fmt(o.paid);
  $("s-pow").textContent = fmt(o["pow-solved"]);
  $("s-repelled").textContent = fmt((o.rejected || 0) + (o["rate-limited"] || 0) + (o.blocked || 0) + (o.busy || 0));
  renderFeed(visits);
  if (!state.first && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
    const show = visits.slice(-12);
    show.forEach((v, i) => setTimeout(() => spawn(v), (i * 1000) / show.length));
  }
  state.first = false;
}

function renderFeed(visits) {
  const feed = $("feed");
  for (const v of visits) {
    const tr = el("tr");
    tr.append(el("td", time(v.time)), el("td", who(v)), el("td", v.path), el("td", OUTCOME[v.outcome] || v.outcome, GROUP[v.outcome]));
    feed.prepend(tr);
  }
  while (feed.rows.length > 10) feed.deleteRow(feed.rows.length - 1);
}

// A visitor walks from the left to the wall, then passes the gate into the
// site or fades at the wall (repelled ones drift back). Names show on hover,
// or next to the dot when only a few are on screen.
const SVGNS = "http://www.w3.org/2000/svg";
const WALL_X = 610, GATE_Y = 205, MAX_SPRITES = 60, LABEL_BELOW = 6;

function spawn(v) {
  if (state.sprites.length >= MAX_SPRITES) state.sprites.shift().g.remove();
  const group = GROUP[v.outcome] || "held";
  const g = document.createElementNS(SVGNS, "g"); g.setAttribute("class", "sprite");
  const c = document.createElementNS(SVGNS, "circle"); c.setAttribute("r", 6); c.setAttribute("fill", COLOR[group]);
  const title = document.createElementNS(SVGNS, "title"); title.textContent = who(v) + " · " + (OUTCOME[v.outcome] || v.outcome);
  const label = document.createElementNS(SVGNS, "text"); label.setAttribute("x", 10); label.setAttribute("y", 4);
  label.textContent = v.bot ? who(v) : "";
  g.append(c, title, label);
  $("sprites").append(g);
  const y0 = 40 + Math.random() * 320;
  state.sprites.push({ g, label, group, t0: performance.now(), x0: 20 + Math.random() * 60, y0, ty: group === "paid" || group === "pow" ? GATE_Y + (Math.random() * 30 - 15) : y0 });
}

const ease = (t) => (t < 0 ? 0 : t > 1 ? 1 : 1 - Math.pow(1 - t, 2));
function frame(now) {
  const few = state.sprites.length <= LABEL_BELOW;
  state.sprites = state.sprites.filter((s) => {
    const t = now - s.t0, walk = 2600, after = t - walk;
    let x, y = s.ty, op = 1;
    if (after < 0) { const k = ease(t / walk); x = s.x0 + (WALL_X - s.x0) * k; y = s.y0 + (s.ty - s.y0) * k; }
    else if (s.group === "paid" || s.group === "pow") {
      const k = ease(after / 1800); x = WALL_X + (860 - WALL_X) * k; y = s.ty + (240 - s.ty) * k; op = 1 - Math.max(0, (after - 1000) / 800);
    } else {
      const k = ease(after / 1500); x = WALL_X - (s.group === "repelled" ? 80 * k : 0); op = 1 - k;
    }
    s.label.style.display = few ? "" : "none";
    s.g.setAttribute("transform", `translate(${x.toFixed(1)},${y.toFixed(1)})`);
    s.g.setAttribute("opacity", Math.max(0, op).toFixed(2));
    if (op <= 0) { s.g.remove(); return false; }
    return true;
  });
  if (state.key) requestAnimationFrame(frame);
}

// --- node indicator and settings page ---
async function refresh() {
  let st;
  try { st = await api("GET", "/api/status"); } catch { return; }
  const n = st.gate.node, s = st.settings;
  const bad = n.breaker_open || st.node_error;
  $("node").className = "node " + (bad ? "bad" : "ok");
  $("node-text").textContent = bad ? "Lightning: problem" : "Lightning OK";
  $("node").title = n.breaker_open ? "Paused after failures until " + time(n.breaker_open_until)
    : st.node_error || n.last_error || (st.node ? fmt(st.node.balance_sat) + " sats, " + st.node.channels + " channels" : s.lightning_backend);
  state.status = st;
  if (!$("settings").hidden) renderCurrent(st);
  if (st.node) $("wd-balance").textContent = fmt(st.node.balance_sat);
  $("wd-dest").textContent = s.withdraw_destination || "";
  $("pause-banner").hidden = !s.paused;
  document.body.classList.toggle("paused", !!s.paused);
}

function renderCurrent(st) {
  const s = st.settings, n = st.gate.node, p = s.current_pow;
  const list = [["Origin", s.origin], ["Paused", s.paused ? "yes (protected paths are free)" : "no"],
    ["Protected", s.protect.join(" ")], ["Price", s.price_sats + " sats"],
    ["Path prices", (s.prices || []).map((r) => r.pattern + " = " + r.price_sats + " sats").join(", ") || "none (default only)"],
    ["Proof-of-work", `${p.Algorithm} difficulty ${p.Difficulty}` + (p.MemoryMiB ? `, ${p.MemoryMiB} MiB` : "")],
    ["Invoices / min (all)", s.max_invoices_per_minute], ["Bot identification", s.identify_bots ? "on" : "off"],
    ["Visitor IPs in monitor", s.visitor_ips], ["AI may apply changes", s.ai_can_apply ? "yes" : "no (propose only)"],
    ["Lightning backend", s.lightning_backend], ["Invoices created / failed", fmt(n.invoices_created) + " / " + fmt(n.invoice_failures)],
    ["Open invoices", fmt(st.gate.open_invoices)]];
  if (n.last_error) list.push(["Last Lightning error", n.last_error + " (" + time(n.last_error_at) + ")"]);
  rows($("set-current"), list);
}

async function loadSettings() {
  let s;
  try { s = await api("GET", "/api/settings"); } catch { return; }
  fillSettings(s);
  if (state.status) renderCurrent(state.status);
}
const POLICY_CLASSES = ["unknown", "impostor", "verified", "verified-signed"];
const POLICY_ACTIONS = [["pay", "Pay"], ["pow-only", "PoW only"], ["free", "Free"], ["block", "Block"]];

function fillSettings(s) {
  $("f-price").value = s.price_sats; $("f-sps").value = s.pow_seconds_per_sat; $("f-cpm").value = s.challenges_per_minute;
  $("f-pause").checked = !!s.paused;
  $("f-pause").disabled = state.role !== "admin";
  fillProtect(s.protect);
  fillPrices(s.prices);
  fillPolicies(s.bot_policies);
  state.current = s;
}

function fillPolicies(pol) {
  pol = pol || {};
  for (const k of POLICY_CLASSES) {
    const sel = $("pol-" + k);
    if (!sel.options.length) {
      for (const [value, label] of POLICY_ACTIONS) {
        const o = document.createElement("option");
        o.value = value; o.textContent = label;
        sel.append(o);
      }
    }
    sel.value = (pol[k] && pol[k].action) || "pay";
  }
}
function readPolicies() {
  const out = {};
  const cur = (state.current && state.current.bot_policies) || {};
  for (const k of POLICY_CLASSES) {
    const action = $("pol-" + k).value;
    const prev = cur[k] || {};
    const mult = prev.price_multiplier;
    if (action !== "pay") {
      out[k] = { action };
      if (mult && mult !== 1) out[k].price_multiplier = mult;
    } else if (mult && mult !== 1) {
      out[k] = { action: "pay", price_multiplier: mult };
    }
  }
  return out;
}
function classPolicies(pol) {
  const out = {};
  for (const k of POLICY_CLASSES) {
    if (pol && pol[k]) out[k] = pol[k];
  }
  return out;
}

function fillProtect(list) {
  $("f-protect").replaceChildren();
  const paths = list && list.length ? list : [""];
  paths.forEach((p) => $("f-protect").append(protectRow(p)));
}
function protectRow(pattern) {
  const tr = el("tr");
  const pat = document.createElement("input");
  pat.className = "pat"; pat.placeholder = "/premium/**"; pat.value = pattern || "";
  const actions = el("td", null, "actions");
  const up = el("button", "↑"); up.type = "button"; up.title = "Move up";
  const down = el("button", "↓"); down.type = "button"; down.title = "Move down";
  const del = el("button", "Remove"); del.type = "button";
  up.addEventListener("click", () => { const prev = tr.previousElementSibling; if (prev) tr.parentNode.insertBefore(tr, prev); });
  down.addEventListener("click", () => { const next = tr.nextElementSibling; if (next) tr.parentNode.insertBefore(next, tr); });
  del.addEventListener("click", () => { if ($("f-protect").rows.length > 1) tr.remove(); });
  actions.append(up, " ", down, " ", del);
  const tdPat = el("td"); tdPat.append(pat);
  tr.append(tdPat, actions);
  return tr;
}
function readProtect() {
  return [...$("f-protect").rows].map((tr) => tr.querySelector(".pat").value.trim()).filter((p) => p);
}

function fillPrices(list) {
  $("f-prices").replaceChildren();
  (list || []).forEach((r) => $("f-prices").append(priceRow(r.pattern, r.price_sats)));
}
function priceRow(pattern, sats) {
  const tr = el("tr");
  const pat = document.createElement("input");
  pat.className = "pat"; pat.placeholder = "/premium/**"; pat.value = pattern || "";
  const price = document.createElement("input");
  price.className = "sats"; price.type = "number"; price.min = "1"; price.max = "1000000";
  price.value = sats || 1;
  const actions = el("td", null, "actions");
  const up = el("button", "↑"); up.type = "button"; up.title = "Move up";
  const down = el("button", "↓"); down.type = "button"; down.title = "Move down";
  const del = el("button", "Remove"); del.type = "button";
  up.addEventListener("click", () => { const prev = tr.previousElementSibling; if (prev) tr.parentNode.insertBefore(tr, prev); });
  down.addEventListener("click", () => { const next = tr.nextElementSibling; if (next) tr.parentNode.insertBefore(next, tr); });
  del.addEventListener("click", () => tr.remove());
  actions.append(up, " ", down, " ", del);
  const tdPat = el("td"); tdPat.append(pat);
  const tdSats = el("td"); tdSats.append(price);
  tr.append(tdPat, tdSats, actions);
  return tr;
}
function readPrices() {
  return [...$("f-prices").rows].map((tr) => ({
    pattern: tr.querySelector(".pat").value.trim(),
    price_sats: +tr.querySelector(".sats").value,
  }));
}

$("f-price-add").addEventListener("click", () => $("f-prices").append(priceRow("", 1)));
$("f-protect-add").addEventListener("click", () => $("f-protect").append(protectRow("")));

function weakenReasons(cur, ch) {
  const why = [];
  if (ch.paused === true && !cur.paused) why.push("protected paths would be served free (pause)");
  if (ch.paused === false && cur.paused) why.push("protection would start charging again (unpause)");
  if (ch.price_sats != null && ch.price_sats < cur.price_sats) why.push("the default price would fall");
  if (ch.pow_seconds_per_sat != null && ch.pow_seconds_per_sat < cur.pow_seconds_per_sat) why.push("proof-of-work would get easier");
  if (ch.challenges_per_minute != null && ch.challenges_per_minute > cur.challenges_per_minute) why.push("the per-client rate limit would be raised");
  if (ch.protect) {
    const next = new Set(ch.protect);
    for (const p of cur.protect || []) {
      if (!next.has(p)) { why.push("a protect pattern would be removed or narrowed"); break; }
    }
  }
  if (ch.prices) {
    const oldMin = Math.min(cur.price_sats, ...(cur.prices || []).map((r) => r.price_sats));
    const newMin = Math.min(ch.prices.length ? Infinity : cur.price_sats, ...ch.prices.map((r) => r.price_sats));
    if (newMin < oldMin) why.push("a path price would fall");
  }
  if (ch.bot_policies) {
    for (const [k, v] of Object.entries(ch.bot_policies)) {
      if (v && v.action === "free") why.push("a bot policy would be set to free (" + k + ")");
    }
  }
  return why;
}

$("set-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const cur = state.current, ch = {};
  try {
    if ($("f-pause").checked !== !!cur.paused) ch.paused = $("f-pause").checked;
    if (+$("f-price").value !== cur.price_sats) ch.price_sats = +$("f-price").value;
    const protect = readProtect();
    if (JSON.stringify(protect) !== JSON.stringify(cur.protect || [])) ch.protect = protect;
    const prices = readPrices();
    if (JSON.stringify(prices) !== JSON.stringify(cur.prices || [])) ch.prices = prices;
    if (+$("f-sps").value !== cur.pow_seconds_per_sat) ch.pow_seconds_per_sat = +$("f-sps").value;
    if (+$("f-cpm").value !== cur.challenges_per_minute) ch.challenges_per_minute = +$("f-cpm").value;
    const pol = readPolicies();
    if (JSON.stringify(pol) !== JSON.stringify(classPolicies(cur.bot_policies))) ch.bot_policies = pol;
    if (!Object.keys(ch).length) return msg("set-msg", "Nothing changed.");
    const why = weakenReasons(cur, ch);
    if (state.role !== "admin") {
      if (why.length) {
        await api("POST", "/api/proposals", { changes: ch, reason: "would reduce protection: " + why.join("; ") });
        msg("set-msg", "Saved as a suggestion for the human admin (it would reduce protection).", "ok");
        return loadProposals();
      }
    } else if (why.length && !confirm("This would reduce protection:\n\n- " + why.join("\n- ") + "\n\nApply it anyway?")) {
      return;
    }
    fillSettings(await api("PUT", "/api/settings", ch));
    msg("set-msg", "Applied and saved.", "ok");
  } catch (err) {
    if (state.role !== "admin" && /propose it for the human admin/.test(err.message)) {
      try {
        await api("POST", "/api/proposals", { changes: ch, reason: "would reduce protection" });
        msg("set-msg", "Saved as a suggestion for the human admin (it would reduce protection).", "ok");
        return loadProposals();
      } catch (e2) { msg("set-msg", e2.message, "err"); return; }
    }
    msg("set-msg", err.message, "err");
  }
});

async function loadProposals() {
  let list;
  try { list = await api("GET", "/api/proposals"); } catch { return; }
  const box = $("prop-list");
  if (!list.length) return box.replaceChildren(el("p", "None. The AI admin (if you use one) can only suggest; nothing changes until you approve.", "note"));
  box.replaceChildren(...list.map((p) => {
    const d = el("div", null, "proposal");
    d.append(el("p", `${time(p.created)} · ${p.status}` + (p.reason ? " · " + p.reason : "") + (p.result ? " · " + p.result : "")));
    d.append(el("pre", JSON.stringify(p.changes)));
    if (p.status === "pending") {
      for (const [act, label] of [["approve", "Approve"], ["reject", "Reject"]]) {
        const b = el("button", label);
        b.addEventListener("click", async () => {
          try {
            if (act === "approve") {
              const why = weakenReasons(state.current || {}, p.changes || {});
              if (why.length && !confirm("This proposal would reduce protection:\n\n- " + why.join("\n- ") + "\n\nApprove it anyway?")) return;
            }
            await api("POST", `/api/proposals/${encodeURIComponent(p.id)}/${act}`);
            loadSettings(); loadProposals();
          } catch (err) { alert(err.message); }
        });
        d.append(b, " ");
      }
    }
    return d;
  }));
}

async function loadWithdrawals() {
  if ($("withdraw").hidden) return;
  let w;
  try { w = await api("GET", "/api/withdraw"); } catch { return; }
  const list = (w.history || []).slice(-5).reverse().map((h) => [time(h.time), fmt(h.amount_sat) + " sats", (h.unknown ? "? " : h.ok ? "✓ " : "✗ ") + h.result]);
  $("wd-unknown").hidden = !w.unresolved;
  if (w.locked_until) list.unshift(["Locked", "until " + time(w.locked_until), ""]);
  rows($("wd-history"), list);
}

let prepared = null;
$("wd-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  msg("wd-msg", "");
  try {
    prepared = await api("POST", "/api/withdraw/prepare", { amount_sat: +$("wd-amount").value, password: $("wd-pw").value });
    $("wd-pw").value = "";
    $("wd-c-amount").textContent = fmt(prepared.amount_sat) + (prepared.fee_sat ? " (+ about " + fmt(prepared.fee_sat) + " sats fee)" : "");
    $("wd-c-dest").textContent = prepared.destination;
    $("wd-c-exp").textContent = time(prepared.expires_at);
    $("wd-form").hidden = true; $("wd-confirm").hidden = false;
  } catch (err) { msg("wd-msg", err.message, "err"); }
});
$("wd-cancel").addEventListener("click", () => { prepared = null; $("wd-confirm").hidden = true; $("wd-form").hidden = false; });
$("wd-confirm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const amount = +$("wd-amount2").value;
  if (!prepared || amount !== prepared.amount_sat) return msg("wd-msg", "The amount does not match.", "err");
  if (!confirm(`Last check: send ${fmt(amount)} sats to ${prepared.destination}? This cannot be undone.`)) return;
  try {
    const r = await api("POST", "/api/withdraw/confirm", { confirm_id: prepared.confirm_id, amount_sat: amount, password: $("wd-pw2").value });
    msg("wd-msg", "Sent: " + r.result, "ok");
  } catch (err) { msg("wd-msg", err.message, "err"); }
  $("wd-pw2").value = ""; $("wd-amount2").value = ""; prepared = null;
  $("wd-confirm").hidden = true; $("wd-form").hidden = false; refresh(); loadWithdrawals();
});

async function resolveWithdrawal(sent) {
  if (!confirm(sent ? "You checked the node and the payment WAS sent?" : "You checked the node and the payment was NOT sent?")) return;
  try {
    await api("POST", "/api/withdraw/resolve", { sent, password: $("wd-pw3").value });
    msg("wd-msg", "Recorded.", "ok");
  } catch (err) { msg("wd-msg", err.message, "err"); }
  $("wd-pw3").value = ""; loadWithdrawals();
}
$("wd-sent").addEventListener("click", () => resolveWithdrawal(true));
$("wd-notsent").addEventListener("click", () => resolveWithdrawal(false));

if (state.key) login();
