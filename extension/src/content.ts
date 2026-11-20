/**
 * Isolated content script: a bridge between the page (inject-webln) and
 * the extension background. The origin sent with every request is the
 * page's origin, not a value the page can invent in the background check
 * (we also pass sender.tab.url as a cross-check if present).
 */
const SOURCE = "spikes402-webln";
const REPLY = "spikes402-webln-reply";

window.addEventListener("message", (ev: MessageEvent) => {
  const d = ev.data;
  if (ev.source !== window || !d || d.source !== SOURCE || typeof d.id !== "number") return;
  const origin = location.origin;
  chrome.runtime.sendMessage(
    { type: "webln", method: d.method, origin, invoice: d.invoice },
    (res) => {
      const error = chrome.runtime.lastError?.message ?? (res && res.error);
      window.postMessage(
        { source: REPLY, id: d.id, result: error ? undefined : res, error: error || undefined },
        "*",
      );
    },
  );
});
