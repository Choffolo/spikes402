# Chrome Web Store listing materials

Everything needed to fill in the Chrome Web Store (CWS) developer dashboard for
**Spikes402 Wallet** (extension version 0.1.6). Nothing here is shipped inside the
extension; it is the text to paste into the dashboard, plus the images to upload.

Keep this in step with `extension/src/manifest.json`, `extension/README.md` and the
public [privacy policy](https://spikes402.dev/privacy/) (`demo/site/privacy/index.html`).
If one changes, check the others.

| Item | File / section |
| --- | --- |
| Screenshots (1280x800) | `screenshots/01-…05-*.png` |
| Small promo tile (440x280, optional) | `screenshots/promo-tile-440x280.png` |
| Store icon (128x128) | `../icons/icon-128.png` |
| Regenerate the images | `make-screenshots.mjs` (see [Images](#images)) |
| Upload package | `npm run pack` (see [Upload package](#upload-package)) |

## Listing

**Name:** Spikes402 Wallet

**Summary** (the dashboard reads it from the manifest `description`, 110 characters, limit 132):

> Non-custodial Lightning wallet for Spikes402 pages. Keys stay in this browser; no Spikes402 server or API key.

**Category:** Developer Tools. (It is for people trying Spikes402-protected pages and for
site operators testing them. "Productivity" would also be accepted; do not pick a
category that suggests a mainstream finance product.)

**Language:** English.

**Detailed description:**

```text
Spikes402 Wallet is an early prototype: a small Lightning wallet that lives in your browser and lets web pages protected by Spikes402 (an open-source "pay or do some work" gate based on HTTP 402 and L402) take a few sats from you automatically, within limits you set.

This is unreviewed prototype software. On real bitcoin, use tiny amounts only, and do not keep more in it than you can afford to lose. A test-network mode with no real money is built in.

WHAT IT DOES
- Keeps a non-custodial Lightning wallet in your browser, built on the open-source Spark SDK. Your recovery phrase is encrypted with your password and never leaves your computer. There is no account, no email, no API key and no Spikes402 server for the wallet.
- Lets a website pay through the standard WebLN interface (window.webln), so a Spikes402 page that costs a couple of sats can open without a manual copy-and-paste.
- Shows the price of the current page on the toolbar icon.
- Receive and send: create a Lightning invoice, pay an invoice, or pay a Lightning address.
- A Spikes402 page can also be opened without paying by solving a short proof-of-work puzzle; no wallet is needed for that.

HOW IT KEEPS YOU SAFE
- A site can use the wallet only after you allow it, and you choose its daily limit in sats.
- Default limits are small: 10 sats per page, 100 sats per site per day, 500 sats per day in total. Fees count towards the limits.
- Anything over a limit stops and asks you first.
- The same page is not paid again automatically for 60 seconds, and a site gets at most one automatic payment every 5 seconds.
- Invoices that are already paid, for another network, or expired are refused.
- A new wallet is unusable until you prove you wrote down your 12 recovery words. The wallet locks itself after 15 minutes.
- A history of the last 20 payments (site, time, price, fee, total), and a list of allowed sites you can remove at any time.

PRIVACY
There is no Spikes402 server in the loop: we receive nothing about you. No analytics, no ads. When you send or receive, the Spark SDK talks to the public Spark network (Lightspark, Breez, Flashnet operators; mempool.space for chain lookups). Full details, including what is stored on your computer and how to delete it: https://spikes402.dev/privacy/

OPEN SOURCE
Free software (AGPL-3.0). Source code, build instructions and issue tracker: https://github.com/Choffolo/spikes402 (folder: extension)
```

**Links**
- Homepage: https://spikes402.dev/
- Support / issues: https://github.com/Choffolo/spikes402/issues
- Privacy policy: https://spikes402.dev/privacy/

## Single purpose

> A non-custodial Lightning wallet for paying Spikes402 402 pages (and for sending or
> receiving small Lightning amounts from the popup).

## Permission justifications

Paste each one into the matching field.

**`storage`**
> Stores the wallet on this computer: the recovery phrase encrypted with the user's password, the settings (network, spending limits, allowed and refused sites), how much was spent today, the last 20 payments, and a short record used to stop repeated payments. Nothing is sent to us.

**`offscreen`**
> The wallet library (Spark SDK with its bundled WebAssembly) needs a long-lived document with DOM and WASM support, which the Manifest V3 service worker cannot provide. The extension opens one hidden offscreen document that holds the unlocked wallet and locks itself after 15 minutes of inactivity.

**`notifications`**
> Shows a local notification when a site asks for permission to pay or when a payment needs the user's confirmation, because the popup is closed at that moment. Notifications stay on the computer.

**Host permissions** (`https://api.lightspark.com/*`, `wss://api.lightspark.com/*`, `https://0.spark.lightspark.com/*`, `https://spark-operator.breez.technology/*`, `https://2.spark.flashnet.xyz/*`, `https://mempool.space/*`, `https://regtest-mempool.us-west-2.sparkinfra.net/*`)
> These are the default endpoints of the official Spark SDK, which the extension bundles: the Lightning service provider (api.lightspark.com, including its WebSocket for updates), the three Spark operators, and the chain-lookup hosts (mempool.space on mainnet; a Spark-run host on the test network). They are needed to create invoices, send and receive payments and check deposits. They are data endpoints, not places that code is loaded from. The extension makes no other fixed network calls; when the user sends to a Lightning address (name@domain) it contacts that domain only, over HTTPS, to request an invoice.

**Content scripts on `https://*/*` (plus `http://localhost/*` and `http://127.0.0.1/*` for development)**
> The extension provides the standard WebLN interface (`window.webln`) so any website that charges through a Lightning invoice can ask the wallet to pay, and a Spikes402 page can be on any domain, so the set of pages cannot be listed in advance. One script (MAIN world) defines `window.webln`; the other (isolated world) passes requests to the wallet. They do nothing until a page asks. A site can spend nothing until the user allows that site and sets its daily limit. The isolated script reads only one thing from the page: the Lightning invoice in the element `#webln-pay` or in a `lightning:` link, to show its price on the toolbar icon and to pay it. It sends no page content, browsing history or other page data anywhere, and plain `http:` pages (other than localhost) are not matched.

(There is no `tabs`, `activeTab`, `scripting`, `cookies` or `webRequest` permission.)

## Remote code

**Are you using remote code?** No.

> All JavaScript and WebAssembly (including the Spark SDK and its WASM) is bundled in the package at build time and is not loaded or evaluated from a remote source. The extension-page CSP is `script-src 'self' 'wasm-unsafe-eval'`; `wasm-unsafe-eval` only allows the bundled WASM to run. The network hosts above are data endpoints.

## Privacy practices tab

Answers below match the [privacy policy](https://spikes402.dev/privacy/). Everything is
stored locally; the data we do not receive is still disclosed, because the extension handles it.

**Data types to tick**
- Personally identifiable information: **no**.
- Health information: **no**.
- Financial and payment information: **yes** (Lightning invoices, payment amounts and history, balance; stored locally and sent to the Spark network to make payments).
- Authentication information: **yes** (the password-encrypted recovery phrase, stored locally).
- Personal communications: **no**.
- Location: **no**.
- Web history: not collected as a list of visited pages. The extension stores only the sites the user allowed or refused and the origin of payments made or declined. Tick it if the dashboard's definition would cover that; the policy says so either way.
- User activity (clicks, keystrokes, mouse): **no**.
- Website content: **yes**, narrowly (the Lightning invoice on a page, read locally to show and pay its price).

**Certifications** (all three can be ticked, they are true)
- I do not sell or transfer user data to third parties, outside of the approved use cases (the only transfers are the payment data the user asks the Spark SDK to send).
- I do not use or transfer user data for purposes unrelated to the item's single purpose.
- I do not use or transfer user data to determine creditworthiness or for lending purposes.

**Privacy policy URL:** https://spikes402.dev/privacy/

## Reviewer test instructions

Paste into "Test instructions" (this field is not public). Do **not** put a recovery phrase
or any secret here.

```text
No account or sign-in is needed. The extension is a self-custodial wallet; everything below works without contacting us.

1. The recommended way to try the demo, with no wallet funds and no real money, is the proof-of-work path. Visit https://spikes402.dev/premium/ . The page returns HTTP 402 and offers a Lightning invoice (2 sats) or a short proof-of-work puzzle. Solve the puzzle shown on the page; the premium content opens and no payment is made.

2. Optional, to look at the wallet itself: open the extension popup, choose "Create a new wallet", choose any password (8+ characters), write down the 12 words shown (they only protect this empty test wallet), type the 3 words it asks for, then the wallet opens. In Settings, set the network to "Test network (no real money)", save, lock and unlock. "Receive" then creates a test-network invoice.

3. To see the first-time consent prompt without paying: with the wallet unlocked (any network), visit https://spikes402.dev/premium/ again. The extension asks "Allow this site to pay?" with a daily limit. Click "Allow" or "Don't allow", then open the popup: "Trusted sites" lists the choice and lets you remove it. The demo runs on real bitcoin (mainnet) and costs 2 sats, so a test-network wallet refuses the invoice with a message that it is for another Bitcoin network; this is the intended safety check.

4. Other checks in the popup: "Limits" (default 10 sats per page, 100 per site per day, 500 per day), "Transaction history", and the recovery-words screen (asks for the password).
```

A funded throw-away wallet (a few dozen sats on mainnet, so a reviewer can see a 2-sat
payment succeed) is optional and is the publisher's choice if they want to offer it. It is
never stored in this repository. If used, put the recovery phrase only in the private
"Test instructions" field, and only for a wallet that holds a tiny amount.

## Upload package

```sh
cd extension
npm ci
npm run pack      # type-checks, builds dist/, writes spikes402-extension.zip
```

`extension/spikes402-extension.zip` is the file to upload. Do not commit it (see
`CONTRIBUTING.md`). `manifest.json` must be at the root of the zip, which `scripts/pack.mjs`
does. About 3 MB (the Spark WASM is most of it). Bump `version` in
`extension/src/manifest.json` and `extension/package.json` for every upload; the store
rejects a version it already has.

## Images

CWS wants a 128x128 icon (already `../icons/icon-128.png`), at least one and at most five
screenshots of 1280x800 or 640x400, and optionally a 440x280 small promo tile.

The screenshots are the real popup (`dist/index.html`, `popup.js`, `popup.css`) rendered
headlessly with a mocked `chrome.runtime` and made-up test-network data (a 4,210 sat
test balance, the site `spikes402.dev`). No real wallet or recovery phrase is involved.
Regenerate them after any popup change:

```sh
cd extension
npm run build
npm i --no-save playwright-core        # not a project dependency
CHROME=/usr/bin/google-chrome node store/make-screenshots.mjs
```

| File | Shows |
| --- | --- |
| `01-locked-wallet.png` | Locked wallet, test network, "no account" message |
| `02-first-time-site-consent.png` | First-time "Allow this site to pay?" prompt with the daily limit |
| `03-confirm-repeat-payment.png` | A repeat payment stopping to ask |
| `04-limits-and-trusted-sites.png` | Default limits and the trusted-sites list |
| `05-transaction-history.png` | Transaction history |

Not included: a screenshot of the toolbar price badge on a 402 page, since that needs a
live extension and the demo page. If wanted, take it by hand (1280x800) with the extension
loaded and the test network selected.

## Submission checklist (for the account owner)

Things only the account owner can do or decide. Nothing here is tax or legal advice.

1. Register a Chrome Web Store developer account (https://chrome.google.com/webstore/devconsole) with a Google account, accept the developer agreement and pay the one-time registration fee (the amount is shown at registration). Italy is on the supported-countries list.
2. Turn on 2-step verification on that Google account; the dashboard may require it before you can publish.
3. In the account settings, choose the **trader or non-trader** status and fill in the contact details that status requires. This is a legal and tax question about how you publish; decide it yourself, and take advice if unsure. The extension is free, so this concerns the account, not payments.
4. Verify the contact email shown in the account.
5. Upload `spikes402-extension.zip` ("New item").
6. Store listing tab: paste the name, description and category above; upload the screenshots and (optionally) the promo tile; the 128x128 icon comes from the package.
7. Privacy practices tab: paste the single purpose, the permission justifications and the remote-code answer; tick the data types and certifications; add the privacy policy URL.
8. Distribution tab: choose visibility and regions. Consider **Unlisted** at first (usable by link, not searchable) for a prototype.
9. Test instructions: paste the block above.
10. Choose whether to publish automatically after approval or to stage it ("deferred publishing", about 30 days to publish an approved item).
11. Submit for review. It can take days to weeks; broad host access (all https pages) and a wallet can slow it down. The wallet is not in a restricted category, but expect questions.
12. After approval, check the live listing text against the extension, and keep the privacy policy URL working: removing it can get the item taken down.

A new publisher can have at most two items published at first.
