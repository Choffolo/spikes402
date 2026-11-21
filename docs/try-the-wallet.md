# Try the Spikes402 wallet

A short guide to the prototype browser extension (version 0.1.4). **This is
unreviewed prototype software. On mainnet, use only tiny amounts. Do not
store more than you can afford to lose.**

You need [Node.js 22](https://nodejs.org/) or newer to build it, or a zip
someone already built with `npm run pack`.

## Build

```sh
cd extension
npm install
npm test
npm run build
```

That writes `extension/dist/`, which contains `manifest.json`. Optionally,
`npm run pack` also writes `extension/spikes402-extension.zip` (unzip it
into a folder and use that folder in the steps below).

## Load in Chrome

1. Open `chrome://extensions`.
2. Turn on **Developer mode**.
3. Click **Load unpacked** and choose the `extension/dist` folder (the one
   that contains `manifest.json`).
4. Pin the extension.

## Load in Firefox (experimental, untested)

Nobody has run this wallet in a real Firefox yet, so treat it as a way to
help test, not as a supported browser. Firefox 128 or newer is required.
Build it with `npm run build:firefox` instead of `npm run build` (Chrome
and Firefox need different manifests).

1. Open `about:debugging#/runtime/this-firefox`.
2. Click **Load Temporary Add-on…** and select
   `extension/dist/manifest.json`.

Firefox opens a small "wallet host" window instead of a hidden document.
Leave it open while you want the wallet unlocked. If a payment needs
confirmation, the toolbar badge turns amber; open the popup yourself.

## Create or unlock a wallet

Open the popup and choose **Create a new wallet** or **Restore from 12
words**. Then choose a password (at least 8 characters) and type it again.

For a new wallet, **write the 12 words on paper and keep them safe.** Anyone
who has them can spend your funds. Then type 3 of them back when asked: the
wallet only opens after that. If you click outside the popup it closes; open
it again, enter your password, and you continue where you stopped. You can
see the words again later under **Recovery words** in the popup (it asks for
your password).

Unlock with your password and press Enter, or click Unlock. The wallet
locks itself after 15 minutes of inactivity, or when you click Lock.

## Top up a few sats

In the popup, under Receive, enter a small amount and create a Lightning
invoice. Pay it from a wallet you already have (for example Phoenix).
Use **mainnet** for a real top-up; Spark **regtest** is for development and
does not move real money. Paying or topping up from Phoenix needs mainnet.

## Send

Under Send, paste a Lightning invoice, or type a Lightning address such as
`name@domain.com` and how many sats. You see "Sending…", then "Sent N sats
(fee M)" or a clear error, and the balance updates. Addresses that only work
with newer "BOLT12" wallets (for example `name@phoenixwallet.me`) are not
supported yet: paste an invoice from that wallet instead.

## Limits and a 402 page

Defaults (you can change them in the popup):

- 10 sats per page, **fee included**: the price plus the biggest possible fee
  (10% of the price, at least 5 sats) must fit. You only pay the real fee,
  which is often lower, but a page above 5 sats asks first with the default.
- 100 sats per site per day
- 500 sats per day in total

Visit a Spikes402 page over `http:` or `https:` (for example
`http://127.0.0.1:8402/premium/…`; `file:` URLs are not supported). If the
wallet is unlocked and the price fits all three caps, the page is paid
automatically, and the page shows "Paying N sats…" until it opens. If it
would go over a cap, the popup asks (showing "N sats + a fee of up to M")
whether to **pay this time**, **always allow this price on this site**, or
**not pay**. "Always
allow" raises the per-page cap for that site only; daily caps still apply.
If several pages ask at once, the popup shows them one at a time.

If a payment is slow (no answer after 60 seconds) it is not counted as
failed: it stays in your budget until the wallet confirms that it settled
or failed, and the page shows an error. Do not pay the same invoice again.

**Transaction history** in the popup lists the last 20 payments: site, time,
price, fee and total.

The toolbar badge shows the price of the current page, in sats. It stays
on the icon in teal after the page is paid and opens, and clears when you
leave for another address. The icon flashes yellow briefly when a payment
succeeds. Colour: muted for the price, teal after a successful payment,
amber if a confirmation is waiting.

Build notes and security details: [extension/README.md](../extension/README.md).
