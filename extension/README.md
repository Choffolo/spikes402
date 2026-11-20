# Spikes402 Wallet (browser extension)

A small Manifest V3 extension that keeps a **non-custodial** Lightning
wallet in your browser and injects [`window.webln`](https://www.webln.guide/)
so a [Spikes402](https://github.com/Choffolo/spikes402) 402 page can pay
with one click (or automatically, when the amount is within your budget).

Keys never leave this machine. There is no Spikes402 server for the wallet.
The wallet uses the official [Spark SDK](https://docs.spark.money/wallets/typescript)
(`@buildonspark/spark-sdk`, Apache-2.0). No API key and no developer
signup are required.

This is an early prototype. Do not store more than you can afford to lose.
On mainnet, use only tiny amounts.

## What works today

- Create a 12-word recovery phrase locally, or restore one. Confirm you have
  written the words down (they are shown once).
- Encrypt the phrase at rest with your password (WebCrypto: PBKDF2-SHA-256,
  600,000 iterations, AES-256-GCM). Unlock sends the vault and password to
  the wallet host, which decrypts the phrase only there (not in the service
  worker). The host locks itself after 15 minutes of inactivity.
- Popup: balance, Receive (Lightning invoice + QR), Send to a Lightning
  invoice or a Spark address, budget settings, payment history.
- Budgets: max sats per page payment (default 10), max sats per site per
  day (default 100), global daily cap (default 500), auto-pay on/off.
- `window.webln` on http(s) pages: `enable`, `sendPayment`, `getInfo`.
  `sendPayment` decodes the invoice amount, refuses amountless invoices and
  amounts above the per-payment cap, pays silently when auto-pay is on and
  the daily caps still fit, and otherwise asks you to confirm in the popup
  (the page origin is shown). After a Lightning payment the extension waits
  for the payment preimage, then checks locally that `sha256(preimage)`
  equals the invoice's payment hash (needed for WebLN / L402). Daily budget
  caps include the Lightning fee, not only the invoice amount.
- Default network is **Bitcoin mainnet**. Spark **regtest** is available
  for development (no real money). Creating a Lightning invoice on regtest
  works without an API key; paying or topping up from a real wallet such
  as Phoenix needs **mainnet**.

## What is stubbed or limited

- There is no card on-ramp, no community-average pricing, and no time-based
  access (those ideas are parked).
- On-chain Bitcoin addresses are not a send target in this prototype (use
  a Lightning invoice or a Spark address).
- Firefox has no `chrome.offscreen` API. The extension opens a small host
  window instead, so the wallet can keep running. Chrome uses a hidden
  offscreen document. See [Firefox](#firefox).

The Spark SDK is bundled into the extension at build time (including its
local WASM for Spark signing). Nothing in that package is fetched from the
network as code. The wallet then talks to the public Spark operators and
Lightning service provider listed below.

## Network hosts

The extension is allowed to connect only to these HTTPS endpoints (Spark
SDK v0.16.0 defaults). They are infrastructure, not a place we load
JavaScript from:

| Host | Role |
| --- | --- |
| `https://api.lightspark.com` | Spark Service Provider (Lightning send/receive), mainnet and regtest |
| `wss://api.lightspark.com` | SSP GraphQL subscriptions |
| `https://0.spark.lightspark.com` | Spark operator |
| `https://spark-operator.breez.technology` | Spark operator (not the Breez SDK) |
| `https://2.spark.flashnet.xyz` | Spark operator |
| `https://mempool.space` | Mainnet chain lookup (electrs) |
| `https://regtest-mempool.us-west-2.sparkinfra.net` | Regtest chain lookup |

These also appear in `manifest.json` as `host_permissions` and in the
extension page CSP `connect-src`. If the Spark SDK changes its default
hosts, this list and the manifest must be updated together
(`src/spark-util.ts` holds the same list).

## Build

You need Node.js 22 or newer.

```sh
cd extension
npm install
npm test
npm run lint
npm run build
```

`npm run build` type-checks and bundles the extension into `dist/`. The
Spark SDK (including its WASM) is compiled into the offscreen / wallet
host script, not loaded from a CDN.

`npm run pack` also writes `/workspace/spikes402-extension.zip` (a zip of
`dist/`) for copying onto another machine.

`npm run audit` runs `npm audit --omit=dev` on production dependencies.

## Load unpacked in Chrome

1. Build as above, so `extension/dist/manifest.json` exists (or unzip
   `spikes402-extension.zip` into a folder).
2. Open `chrome://extensions`.
3. Turn on **Developer mode**.
4. Click **Load unpacked** and choose the `extension/dist` folder (the one
   that contains `manifest.json`).
5. Pin the extension, open the popup, and create or restore a wallet.

To try the 402 page later, the page must be served over `http:` or
`https:` (the content script does not run on `file:` URLs). A local
Spikes402 proxy such as `http://127.0.0.1:8402/...` is fine.

## Load in Firefox

Firefox 128 or newer is required (`world: "MAIN"` content scripts).

1. Build as above.
2. Open `about:debugging#/runtime/this-firefox`.
3. Click **Load Temporary Add-on…** and select `extension/dist/manifest.json`.

Gaps compared to Chrome:

- No offscreen documents: a small "wallet host" window is opened instead.
  Do not close it while you want the wallet unlocked.
- `chrome.action.openPopup` may be missing; if a payment needs
  confirmation, look for the `!` badge on the toolbar icon and open the
  popup yourself.
- Store review on addons.mozilla.org is stricter for wallets. This
  prototype is meant to be loaded temporarily, not published yet.

## Security notes

- **Content-script isolation.** The page only sees `window.webln`. The
  isolated content script forwards requests. The background takes the
  **real page origin** from the content-script sender (`sender.origin` /
  the tab URL), not from a value the page can invent. Privileged actions
  (unlock, send, confirm, settings) are accepted only from extension
  pages. The mnemonic is decrypted only in the wallet host.
- **Origin in prompts.** Confirmation text and the desktop notification
  include the page origin and the amount, not just the invoice.
- **No remote code.** Manifest V3 CSP for extension pages is
  `script-src 'self' 'wasm-unsafe-eval'` (the Spark SDK instantiates
  bundled WASM for signing; there is no `unsafe-eval` and no remote
  script), plus `frame-ancestors 'none'`. `connect-src` is limited to the
  hosts above. The service worker does not run the wallet; an offscreen
  document (or a Firefox host window) does. The wallet host auto-locks
  after 15 minutes of inactivity.
- **Password.** Choose a unique password. It is not recoverable. The
  recovery phrase is; the backup screen is the only time it is shown after
  a new wallet is created.
- **Mainnet.** This is unreviewed prototype software. Use tiny amounts.

## Tests

Unit tests cover budget decisions (silent pay, confirm, refuse, day reset,
fees on daily caps), BOLT11 amount and payment-hash parsing, local preimage
checks, mnemonic generation and password encryption, sender/origin and
settings checks, and Spark helpers (network mapping, fee cap, address
shape, payment status).

```sh
npm test
```
