// Renders the real popup (extension/dist) with a mocked chrome.runtime and fake,
// test-network data, then composes 1280x800 Chrome Web Store screenshots.
//
//   cd extension && npm run build && npm i --no-save playwright-core
//   CHROME=/usr/bin/google-chrome node store/make-screenshots.mjs
//
// Nothing here talks to a wallet, a network or a real recovery phrase.
import { createServer } from "node:http";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const root = fileURLToPath(new URL("..", import.meta.url));
const dist = join(root, "dist");
const out = join(root, "store", "screenshots");
mkdirSync(out, { recursive: true });

const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".png": "image/png" };
const server = createServer((req, res) => {
  const path = normalize(new URL(req.url, "http://x").pathname).replace(/^(\.\.[/\\])+/, "");
  try {
    const body = readFileSync(join(dist, path === "/" ? "index.html" : path));
    res.writeHead(200, { "content-type": types[extname(path)] ?? "application/octet-stream" }).end(body);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}/`;

const settings = {
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  siteAllowances: {},
  sites: { "spikes402.dev": { trusted: true, dailySats: 100, since: "2026-11-26T12:00:00.000Z" } },
  network: "regtest",
};
const info = (o = {}) => ({
  unlocked: true, hasVault: true, backupPending: false, network: "regtest", balanceSats: 4210,
  lightningReady: true, lightningHint: "Test network: no real money.", ...o,
});
const history = [
  { time: "2026-11-26T15:02:00.000Z", origin: "https://spikes402.dev", amountSats: 2, priceSats: 2, feeSats: 0, destination: "lnbcrt20n1…", result: "sent", detail: "Paid." },
  { time: "2026-11-26T14:41:00.000Z", origin: "https://spikes402.dev", amountSats: 2, priceSats: 2, destination: "lnbcrt20n1…", result: "refused", detail: "You declined the payment." },
];
const states = {
  locked: { info: info({ unlocked: false, balanceSats: undefined }), settings, history: [] },
  consent: {
    info: info(), settings: { ...settings, sites: {} }, history: [],
    pending: { id: "t1", kind: "trust", site: "spikes402.dev", origin: "https://spikes402.dev", invoice: "", amountSats: 0, costSats: 0, reason: "", createdAt: 1 },
    pendingCount: 1,
  },
  confirm: {
    info: info(), settings, history,
    pending: { id: "p1", site: "spikes402.dev", origin: "https://spikes402.dev", invoice: "x", amountSats: 2, costSats: 7, reason: "This page was paid a moment ago and asks again.", createdAt: 1 },
    pendingCount: 1,
  },
  limits: { info: info(), settings, history },
  history: { info: info(), settings, history },
};

const browser = await chromium.launch({ executablePath: process.env.CHROME ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });

/** Render one popup state. `crop` picks a vertical slice by elements; otherwise the whole popup. */
async function popup(name, { click, crop } = {}) {
  const page = await browser.newPage({ viewport: { width: 360, height: 100 }, deviceScaleFactor: 3 });
  await page.addInitScript((snap) => {
    window.chrome = { runtime: { lastError: undefined, sendMessage: (_m, cb) => setTimeout(() => cb(snap), 0) } };
  }, states[name]);
  await page.goto(base + "index.html");
  await page.waitForFunction(() => [...document.querySelectorAll("section")].some((s) => !s.hidden));
  if (click) {
    await page.click("#" + click);
    await page.waitForSelector("#view-history:not([hidden])");
  }
  const box = await page.evaluate((c) => {
    const full = document.documentElement.scrollHeight;
    if (!c) return { y: 0, h: full };
    const top = c.fromText
      ? [...document.querySelectorAll("h2")].find((h) => h.textContent.trim().toLowerCase() === c.fromText).getBoundingClientRect().top + scrollY - 6
      : 0;
    const bottom = document.querySelector(c.toSel).getBoundingClientRect().bottom + scrollY + 10;
    return { y: Math.max(0, top), h: Math.min(full - top, bottom - top) };
  }, crop);
  const png = await page.screenshot({ fullPage: true, clip: { x: 0, y: box.y, width: 360, height: box.h } });
  await page.close();
  return { png, h: box.h };
}

const shots = [
  {
    file: "01-locked-wallet.png", state: "locked",
    title: "A wallet that lives in your browser",
    lines: ["Non-custodial: keys never leave this computer.", "No account, no email, no Spikes402 server.", "Prototype: tiny amounts only."],
  },
  {
    file: "02-first-time-site-consent.png", state: "consent",
    title: "Every site asks first",
    lines: ["A site can pay only after you allow it.", "You choose its daily limit in sats.", "Fees count towards the limit."],
  },
  {
    file: "03-confirm-repeat-payment.png", state: "confirm", crop: { toSel: "#pending-box" },
    title: "Repeat payments stop and ask",
    lines: ["The same page is not paid again by itself for 60 seconds.", "One automatic payment per site every 5 seconds.", "Anything over your limits needs your OK."],
  },
  {
    file: "04-limits-and-trusted-sites.png", state: "limits", crop: { fromText: "limits", toSel: "#sites + p" },
    title: "Small limits by default",
    lines: ["10 sats per page, 100 per site per day, 500 per day in total.", "Remove a trusted site at any time.", "A test-network switch lets you try it without real money."],
  },
  {
    file: "05-transaction-history.png", state: "history", click: "btn-history",
    title: "See what was paid",
    lines: ["Site, time, price, fee and total for the last 20 payments.", "Full invoices and payment proofs are not kept."],
  },
];

const frame = (png, cssH, title, lines) => `<!doctype html><meta charset="utf-8"><style>
html,body{margin:0}body{width:1280px;height:800px;background:#fafaf9;color:#1c1917;font:20px/1.5 system-ui,sans-serif;display:flex;align-items:center;gap:56px;padding:0 88px;box-sizing:border-box}
.t{flex:1}.brand{color:#0f766e;font-weight:700;font-size:18px;letter-spacing:.04em;text-transform:uppercase}
h1{font-size:44px;line-height:1.15;margin:12px 0 28px}ul{padding-left:1.1em;margin:0;color:#44403c}li{margin:10px 0}
.p{flex:none;border:1px solid #d6d3d1;border-radius:14px;box-shadow:0 10px 40px rgba(0,0,0,.14);overflow:hidden;background:#fafaf9}.p img{display:block;height:var(--h);width:auto}
.foot{position:absolute;left:88px;bottom:36px;font-size:15px;color:#57534e}</style>
<div class="t"><div class="brand">Spikes402 Wallet</div><h1>${title}</h1><ul>${lines.map((l) => `<li>${l}</li>`).join("")}</ul></div>
<div class="p" style="--h:${cssH}px"><img src="data:image/png;base64,${png.toString("base64")}"></div>
<div class="foot">Early prototype · test network shown · no real money</div>`;

for (const s of shots) {
  const { png, h } = await popup(s.state, { click: s.click, crop: s.crop });
  // Show the popup larger than life size so the text is readable, but never taller than the canvas allows.
  const cssH = Math.round(h * Math.min(1.8, 640 / h));
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await page.setContent(frame(png, cssH, s.title, s.lines));
  await page.screenshot({ path: join(out, s.file), clip: { x: 0, y: 0, width: 1280, height: 800 } });
  await page.close();
  console.log("wrote", s.file);
}
// Small promo tile, 440x280.
{
  const icon = readFileSync(join(root, "icons", "icon-128.png")).toString("base64");
  const page = await browser.newPage({ viewport: { width: 440, height: 280 } });
  await page.setContent(`<!doctype html><meta charset="utf-8"><style>html,body{margin:0}body{width:440px;height:280px;background:#0f766e;color:#fff;font-family:system-ui,sans-serif;display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center}
img{width:96px;height:96px;border-radius:20px;background:#fff;padding:8px;box-sizing:content-box}h1{font-size:30px;margin:18px 0 6px}p{margin:0;font-size:16px;opacity:.92}</style>
<img src="data:image/png;base64,${icon}"><h1>Spikes402 Wallet</h1><p>Pay Spikes402 pages with Lightning. Keys stay in your browser.</p>`);
  await page.screenshot({ path: join(out, "promo-tile-440x280.png"), clip: { x: 0, y: 0, width: 440, height: 280 } });
  await page.close();
  console.log("wrote promo-tile-440x280.png");
}
await browser.close();
server.close();
