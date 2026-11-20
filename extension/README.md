# Spikes402 Wallet (browser extension)

A small Manifest V3 extension that keeps a **non-custodial** Lightning
wallet in your browser and injects [`window.webln`](https://www.webln.guide/)
so a [Spikes402](https://github.com/Choffolo/spikes402) 402 page can pay
with one click (or automatically, when the amount is within your budget).

Keys never leave this machine. There is no Spikes402 server for the wallet.
The wallet talks only to the [Breez SDK (Spark)](https://sdk-doc-spark.breez.technology/)
network you choose.

This is an early prototype. Do not store more than you can afford to lose.

## What works today

- Create a 12-word recovery phrase locally, or restore one. Confirm you have
  written the words down (they are shown once).
- Encrypt the phrase at rest with your password (WebCrypto: PBKDF2-SHA-256,
  600,000 iterations, AES-256-GCM). Unlock decrypts it only in memory, in
  the wallet host page, not in the service worker.
- Popup: balance, Receive (Lightning invoice + QR), Send to an invoice or
  address, budget settings, payment history.
- Budgets: max sats per page payment (default 10), max sats per site per
  day (default 100), global daily cap (default 500), auto-pay on/off.
- `window.webln` on http(s) pages: `enable`, `sendPayment`, `getInfo`.
  `sendPayment` decodes the invoice amount, refuses amountless invoices and
  amounts above the per-payment cap, pays silently when auto-pay is on and
  the daily caps still fit, and otherwise asks you to confirm in the popup
  (the page origin is shown).
- Default network is **Spark regtest** (no real money). No Breez API key is
  required there.

## What is stubbed or limited

- **Lightning send and receive do not work on regtest.** Breez's Spark
  regtest network has no Lightning. Use it to create a wallet and try the
  UI. To pay a real invoice or to top up from Phoenix, switch to **mainnet**
  and paste a Breez API key (see below). Use tiny amounts only.
- There is no card on-ramp, no community-average pricing, and no time-based
  access (those ideas are parked).
- The Breez WASM and JS are copied into the extension at build time
  (`dist/vendor/breez-sdk`). They are not executed until you unlock a
  wallet. Connecting to Breez on a machine without the key/network it
  expects will surface an error in the popup, not a fake success.
- Firefox has no `chrome.offscreen` API. The extension opens a small host
  window instead, so the WASM wallet can keep running. Chrome uses a hidden
  offscreen document. See [Firefox](#firefox).

## Request a Breez API key (needed for Lightning on mainnet)

The Breez SDK requires an API key to operate on **mainnet**. We do not sign
up for you, and no key is bundled with this repository.

1. Open the request form: [https://breez.technology/contact/apikey](https://breez.technology/contact/apikey)
   (the same form can be posted with
   `curl https://breez.technology/contact/apikey` and fields `fullname`,
   `company`, `email`, and `message` — see
   [Breez's getting started docs](https://sdk-doc-spark.breez.technology/guide/getting_started.html)).
2. Wait for Breez to email the key.
3. In the extension popup, set the network to **mainnet**, paste the key
   (it is stored only in this browser, in `chrome.storage.local`), save,
   then lock and unlock the wallet so the setting is applied.

Until that is done, Receive and paying a Lightning invoice show a clear
error. The rest of the extension (budgets, WebLN injection, history) still
runs.

## Build

You need Node.js 22 or newer.

```sh
cd extension
npm install
npm test
npm run lint
npm run build
```

`npm run build` type-checks, bundles the extension into `dist/`, and copies
the Breez web/WASM files into `dist/vendor/breez-sdk` so they load from the
extension package (not from the network).

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
  isolated content script forwards requests and adds the **real page
  origin**. The mnemonic never enters the page, the content script, or the
  service worker in plaintext (it is decrypted only in the wallet host).
- **Origin in prompts.** Confirmation text includes the page origin, not
  just the invoice.
- **No remote code.** Manifest V3 `script-src 'self' 'wasm-unsafe-eval'`
  for extension pages. The Breez WASM is bundled locally. There is no
  eval of page data and no remote script or WASM fetch for the SDK.
- **Password.** Choose a unique password. It is not recoverable. The
  recovery phrase is; the backup screen is the only time it is shown after
  a new wallet is created.
- **Mainnet.** This is unreviewed prototype software. Use tiny amounts.

## Tests

Unit tests cover budget decisions (silent pay, confirm, refuse, day reset)
and BOLT11 amount parsing (multipliers, rounding up fractional sats,
amountless invoices, testnet/regtest prefixes). Crypto tests cover mnemonic
generation and password encryption.

```sh
npm test
```
