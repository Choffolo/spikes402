/**
 * Isolated content script: a bridge between the page (inject-webln) and
 * the extension background, and a reader of this page's price for the
 * toolbar badge. The origin sent with every request is taken from this
 * frame (location.origin). The background also ignores any origin the
 * page includes and uses sender.origin / sender.tab.url.
 */
import { parseBolt11AmountSats } from "./invoice";

const SOURCE = "spikes402-webln";
const REPLY = "spikes402-webln-reply";
const METHODS = new Set(["enable", "getInfo", "sendPayment"]);

window.addEventListener("message", (ev: MessageEvent) => {
  const d = ev.data;
  if (ev.source !== window || ev.origin !== location.origin) return;
  if (!d || d.source !== SOURCE || typeof d.id !== "number") return;
  if (typeof d.method !== "string" || !METHODS.has(d.method)) return;
  const origin = location.origin;
  chrome.runtime.sendMessage(
    { type: "webln", method: d.method, origin, invoice: d.invoice },
    (res) => {
      const error = chrome.runtime.lastError?.message ?? (res && res.error);
      window.postMessage(
        { source: REPLY, id: d.id, result: error ? undefined : res, error: error || undefined },
        location.origin,
      );
    },
  );
});

function findPagePrice(): number | null {
  const btn = document.getElementById("webln-pay");
  const fromBtn = btn?.getAttribute("data-invoice");
  const fromLink = document.querySelector("a[href^='lightning:']")?.getAttribute("href");
  for (const raw of [fromBtn, fromLink]) {
    if (!raw) continue;
    try {
      const n = parseBolt11AmountSats(raw);
      if (n !== null && n > 0) return n;
    } catch {
      /* not a usable invoice */
    }
  }
  return null;
}

function reportPrice() {
  chrome.runtime.sendMessage({ type: "pagePrice", amountSats: findPagePrice() }, () => {
    void chrome.runtime.lastError;
  });
}

chrome.runtime.sendMessage({ type: "pagePrice", amountSats: null }, () => {
  void chrome.runtime.lastError;
});
document.addEventListener("DOMContentLoaded", reportPrice);
window.addEventListener("pageshow", reportPrice);
if (document.readyState !== "loading") reportPrice();
