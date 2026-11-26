# Spikes402 Wallet (browser extension)

A small Manifest V3 extension that keeps a **non-custodial** Lightning
wallet in your browser and injects [`window.webln`](https://www.webln.guide/)
so a [Spikes402](https://github.com/Choffolo/spikes402) 402 page can pay
automatically when the amount is within your budget, on sites you have
allowed.

Keys never leave this machine. There is no Spikes402 server for the wallet.
The wallet uses the official [Spark SDK](https://docs.spark.money/wallets/typescript)
(`@buildonspark/spark-sdk`, Apache-2.0). No API key and no developer
signup are required.

This is an early prototype. Do not store more than you can afford to lose.
On mainnet, use only tiny amounts. To try it in your browser, see
[Try the wallet](../docs/try-the-wallet.md).
What the wallet stores, who it talks to, and how to delete it:
[spikes402.dev/privacy](https://spikes402.dev/privacy/).

## What works today

- First screen: **Create a new wallet** or **Restore from 12 words**; the
  password (typed twice) comes on the next step. Every password field is a
  real form, so the Enter key submits it.
- A new wallet is **not usable until you prove you saved the words**: after
  the 12 words are shown you must type 3 random ones back. Chrome closes a
  popup when you click outside it, so this step survives that: the wallet
  stays locked and "pending backup" is stored with the encrypted vault; when
  you reopen the popup you enter your password to see the words again and
  finish. (A restored wallet needs no quiz: you already have the words.)
  **Recovery words** in the popup shows them again later, behind your
  password; they are decrypted in the wallet host, shown for 60 seconds at
  most, and never stored in plain text.
- Encrypt the phrase at rest with your password (WebCrypto: PBKDF2-SHA-256,
  600,000 iterations, AES-256-GCM). Unlock sends the vault and password to
  the wallet host, which decrypts the phrase only there (not in the service
  worker). The host locks itself after 15 minutes of inactivity.
- Popup: balance (refreshed every few seconds while the popup is open, so a
  receive or a payment shows without reopening), Receive (Lightning invoice
  + QR), Send, limits, and a separate **Transaction history** screen (last
  20: site, time, price, fee, total; never the invoice or the preimage).
- Send takes a Lightning invoice, a Spark address, or a **Lightning
  address** (`name@domain`, a leading ₿ is fine). A Lightning address is
  paid through LNURL-pay (LUD-16): the wallet fetches
  `https://domain/.well-known/lnurlp/name` (https only, no redirects, 10 s,
  64 KiB), requires the payment link (callback) to be https on the same
  host, checks your amount against the address's minimum and maximum, asks
  for an invoice, and **refuses to pay unless that invoice is for exactly
  your amount**. The same fee cap as for pages applies. Addresses that are
  only a BIP353 / BOLT12 offer (Phoenix addresses such as
  `name@phoenixwallet.me`) cannot be paid: the Spark SDK has no BOLT12, and
  such domains serve no LNURL, so the popup says "This address type isn't
  supported yet. Paste an invoice instead." After a send the popup shows
  "Sending…", then "Sent N sats (fee M)" or the error, clears the form and
  updates the balance.
- Trusted sites: a site can use the wallet only after you allow it. The
  first time a page calls `webln.enable()` or asks for a payment, the popup
  (and a notification) asks "Allow automatic payments on <site>, up to N
  sats a day?"; you can change N before allowing. Until you answer, the page
  gets nothing, whether or not the wallet is unlocked. "Don't allow" is
  remembered so the site does not ask again. The list is under "Trusted
  sites" in the popup; removing a site makes it ask again. A site means the
  registrable domain (eTLD+1), so `shop.example.com` and `pay.example.com`
  are one site with one daily limit. Only https pages are served, plus
  `http://localhost` and `http://127.0.0.1` for development (each local port
  is its own site); the script is not injected into other http pages.
- Limits: max sats per page payment (default 10; an **all-inclusive** cap:
  the price plus the biggest possible fee must fit, otherwise the popup
  asks first, showing "N sats + a fee of up to M"), the daily limit offered
  for a newly trusted site (default 100; each trusted site keeps its own),
  global daily cap (default 500). The biggest fee is 10%
  of the price, at least 5 sats, so with the default cap a page above 5
  sats asks first; you only pay the real fee, which is often lower. Each
  paid page is written to the history with its real fee.
  Pages that fit all three caps are paid automatically, unless one of the
  repeat-payment guards below applies. If a page costs
  more, the popup asks whether to pay once, always allow this price on
  this site, or decline. "Always allow" raises the per-page cap for that
  site only; later visits still respect the daily caps.
- How a payment is booked: the full cost (invoice amount plus the fee cap)
  is counted in the budget **before** the payment is sent, and the same
  fee cap is given to the wallet. When the payment settles, the real cost
  replaces it; when it is confirmed failed, it is released. If the wallet
  does not answer in 60 seconds the payment is **unconfirmed**, not failed:
  it stays counted, the page gets an error, and the extension asks the
  wallet again (on unlock, when the popup opens, and every 30 seconds
  while the worker is awake) until it settles or fails. A payment that
  could not be looked up (for example the browser was closed mid-send)
  stays counted until the end of that day. A slow invoice does not hold
  up other payments.
- Confirmations queue up (up to 10 waiting); the popup shows the oldest
  and how many more wait. Each one expires after 5 minutes on its own; an
  expired or answered confirmation cannot be answered again.
- No flash of the 402 page: when a page will be paid without asking, the
  content script covers it at once with "Paying N sats…" (it asks the
  background, which only checks your limits; it reserves and pays nothing).
  The notice goes away if the payment fails. The wallet also looks for the
  payment result every 0.25 s at first, instead of every second.
- Toolbar badge: the number of sats the current tab's page costs to open
  (read from the 402 invoice on the page). The badge is cleared when you
  navigate away (including after you leave a page you just paid). Colour is
  muted for the price, teal after a successful pay — and it stays teal with
  the price on the unlocked page after the reload, until you go to another
  URL. The toolbar icon also flashes yellow briefly when a payment
  succeeds. Amber if a confirmation is waiting.
- `window.webln` on http(s) pages: `enable`, `sendPayment`, `getInfo`.
  `getInfo` returns only a node alias and the method list (no identity
  key or pubkey). `sendPayment` decodes the invoice amount, verifies the
  BOLT11 bech32 checksum, and refuses amountless invoices. After a
  Lightning payment the extension waits for the payment preimage, then
  checks locally that `sha256(preimage)` equals the invoice's payment
  hash (needed for WebLN / L402). Every cap includes the Lightning fee,
  not only the invoice amount.
- Default network is **Bitcoin mainnet**. Spark **regtest** is available
  for development (no real money). Creating a Lightning invoice on regtest
  works without an API key; paying or topping up from a real wallet such
  as Phoenix needs **mainnet**.

## What is stubbed or limited

- There is no card on-ramp, no community-average pricing, and no time-based
  access (those ideas are parked).
- On-chain Bitcoin addresses are not a send target in this prototype (use
  a Lightning invoice or a Spark address).
- Firefox is **experimental and untested** (nobody has run the wallet in a
  real Firefox yet). It has no `chrome.offscreen` API, so the extension
  would open a small host window instead of Chrome's hidden offscreen
  document. See [Firefox](#firefox).

The Spark SDK is bundled into the extension at build time (including its
local WASM for Spark signing). Nothing in that package is fetched from the
network as code. The wallet then talks to the public Spark operators and
Lightning service provider listed below.

## Network hosts

The Spark SDK connects to these HTTPS endpoints (v0.16.0 defaults). Apart
from them, the wallet only contacts the server of a Lightning address when
you send to one (see Security notes). They are infrastructure, not a place we load
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

These also appear in `manifest.json` as `host_permissions` (the extension
page CSP `connect-src` is wider, see Security notes). If the Spark SDK
changes its default hosts, this list and the manifest must be updated
together (`src/spark-util.ts` holds the same list).

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

`npm run pack` also writes `spikes402-extension.zip` in this directory (a zip of
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

**Experimental and untested.** The Chrome build (`npm run build`) has only
`background.service_worker`, so Chrome shows no manifest warning. Firefox
needs `background.scripts` and an add-on id instead; they live in
`src/manifest.firefox.json` and `npm run build:firefox` merges them into
`dist/manifest.json` (rebuild with `npm run build` for Chrome). The code
has a fallback for the missing offscreen API, but the wallet has
not been run in a real Firefox. Firefox 128 or newer is required
(`world: "MAIN"` content scripts). Event pages can be unloaded when idle,
which may drop a waiting confirmation; treat Firefox as a way to help test,
not as a supported browser.

1. Run `npm run build:firefox`.
2. Open `about:debugging#/runtime/this-firefox`.
3. Click **Load Temporary Add-on…** and select `extension/dist/manifest.json`.

Gaps compared to Chrome:

- No offscreen documents: a small "wallet host" window is opened instead.
  Do not close it while you want the wallet unlocked.
- `chrome.action.openPopup` may be missing; if a payment needs
  confirmation, the toolbar badge turns amber (it still shows the page
  price) and a notification may appear; open the popup yourself.
- Store review on addons.mozilla.org is stricter for wallets. This
  prototype is meant to be loaded temporarily, not published yet.

## Security notes

- **Content-script isolation.** The page only sees `window.webln`. The
  isolated content script forwards requests. The background takes the
  **real page origin** from the content-script sender (`sender.origin` /
  the tab URL), not from a value the page can invent. Privileged actions
  (unlock, send, confirm, settings) are accepted only from extension
  pages. The mnemonic is decrypted only in the wallet host.
- **Per-site trust, keyed by registrable domain.** Nothing is paid and
  `enable()` does not succeed for a site you have not allowed, and an
  unknown site gets the same answer whether the wallet is locked or not.
  Caps and trust are keyed by the registrable domain from the Public Suffix
  List (the [`tldts`](https://github.com/remusao/tldts) library, MIT, no
  dependencies besides its core, pinned), including the list's private
  section, so a hostile site cannot get fresh allowances from new
  subdomains, while `alice.github.io` and `bob.github.io` stay separate. A
  hand-written suffix list would be short but wrong for rules such as
  `*.ck` or `co.uk`, and would go stale; this keeps the data current with
  each release of the dependency. Domains are shown in their ASCII form, so
  look-alike (IDN) names are visible.
- **Repeat payments.** A page that keeps asking for small payments is
  slowed down by guards that sit on top of the caps (`src/guard.ts`):
  after a payment, the same page (site + path) is not paid again
  automatically for 60 seconds, and each site gets at most one automatic
  payment every 5 seconds; in both cases the popup asks instead. An invoice
  (by payment hash) that was paid, or is being paid, is refused and never
  asked about. Invoices for another network than the wallet's (mainnet
  takes only `lnbc…`) and expired invoices are refused. The trust prompt
  says that the site may make several small payments by itself, up to the
  daily limit.
- **Clock changes.** Cooldowns use the highest wall-clock time ever seen
  (saved with the wallet state), and, while the service worker is alive,
  the smaller of the wall-clock and the monotonic (`performance.now`)
  elapsed time. A clock set back therefore never shortens a cooldown, and
  while it is clearly behind the highest time seen (more than 30 seconds),
  nothing is paid automatically: the popup asks each time. The daily
  budget bucket is never replaced by an earlier day: if the date goes back,
  the latest bucket keeps counting. **Residual risk:** a clock set forward
  can start a new daily budget once (and, after a service-worker restart,
  when the monotonic clock is gone, it also ends the cooldowns); setting it
  back afterwards does not reset anything, but the wallet then asks for
  every payment, and keeps counting against that later day, until real time
  catches up. The invoice-expiry check uses the clock as it is now, so a
  clock far ahead makes invoices look expired. Whoever can set the clock
  can also change the extension itself, so this only blunts casual tricks.
- **Origin in prompts.** Confirmation text and the desktop notification
  include the page origin and the amount, not just the invoice.
- **No remote code.** Manifest V3 CSP for extension pages is
  `script-src 'self' 'wasm-unsafe-eval'` (the Spark SDK instantiates
  bundled WASM for signing; there is no `unsafe-eval` and no remote
  script), plus `frame-ancestors 'none'`. `connect-src` allows any `https:`
  host, because a Lightning address can live on any domain; the wallet only
  contacts one when you send to that address (never a redirect, never an
  http URL, callback on the same host), and the Spark hosts above otherwise. The service worker does not run the wallet; an offscreen
  document (or a Firefox host window) does. The wallet host auto-locks
  after 15 minutes of inactivity.
- **Password.** Choose a unique password. It is not recoverable. The
  recovery phrase is: see it under "Recovery words" with your password.
- **Mainnet.** This is unreviewed prototype software. Use tiny amounts.

## Tests

Unit tests cover the storage write queue, budget reservations (unconfirmed
payments, release and settle, fee cap shared with the wallet, no stalling
behind a slow invoice), the confirmation queue and its expiry, the
manifest, and budget decisions (silent pay when a page fits, confirm
when it does not, site allowances, refuse amountless invoices, day reset,
fees on every cap), toolbar badge text (kept after a paid reload, cleared on another URL, yellow icon flash), BOLT11 amount, checksum and
payment-hash parsing, local preimage checks, mnemonic generation and
password encryption, sender/origin and settings checks, and Spark helpers
(network mapping, fee cap, address shape, payment status, faster result
polling), the seed backup (a new wallet stays locked until the quiz is
passed, the pending state is stored, only the popup can confirm or reveal),
Lightning addresses (parsing, same-host https callback, min/max, exact
invoice amount, no redirects), the "will it be paid" check for the Paying
notice, history rows (host only, price, fee, total, no preimage), and the
Chrome and Firefox manifests.

```sh
npm test
```
