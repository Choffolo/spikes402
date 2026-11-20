/**
 * Isolated content script: a bridge between the page (inject-webln) and
 * the extension background. The origin sent with every request is taken
 * from this frame (location.origin). The background also ignores any
 * origin the page includes and uses sender.origin / sender.tab.url.
 */
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
