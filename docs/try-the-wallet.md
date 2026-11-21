# Try the Spikes402 wallet

A short guide to the prototype browser extension (version 0.1.1). **This is
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

## Load in Firefox

Firefox 128 or newer is required.

1. Open `about:debugging#/runtime/this-firefox`.
2. Click **Load Temporary Add-on…** and select
   `extension/dist/manifest.json`.

Firefox opens a small "wallet host" window instead of a hidden document.
Leave it open while you want the wallet unlocked. If a payment needs
confirmation, the toolbar badge turns amber; open the popup yourself.

## Create or unlock a wallet

Open the popup. Create a new wallet, or restore one from a 12-word phrase,
and choose a password (at least 8 characters). **Write the 12 words down
and keep them offline.** They are shown once. Anyone who has them can spend
your funds. They are not sent to a server.

Unlock with your password when you want to pay or to send and receive.
The wallet locks itself after 15 minutes of inactivity, or when you click
Lock.

## Top up a few sats

In the popup, under Receive, enter a small amount and create a Lightning
invoice. Pay it from a wallet you already have (for example Phoenix).
Use **mainnet** for a real top-up; Spark **regtest** is for development and
does not move real money. Paying or topping up from Phoenix needs mainnet.

## Budgets and a 402 page

Defaults (you can change them in the popup):

- 10 sats per page (invoice plus fees)
- 100 sats per site per day
- 500 sats per day in total

Visit a Spikes402 page over `http:` or `https:` (for example
`http://127.0.0.1:8402/premium/…`; `file:` URLs are not supported). If the
wallet is unlocked and the price fits all three caps, the page is paid
automatically. If it would go over a cap, the popup asks you to **pay
once**, **always allow this price on this site**, or **decline**. "Always
allow" raises the per-page cap for that site only; daily caps still apply.

The toolbar badge shows the price of the current page, in sats, and clears
when you leave. Colour: muted for the price, teal after a successful
payment, amber if a confirmation is waiting.

Build notes and security details: [extension/README.md](../extension/README.md).
