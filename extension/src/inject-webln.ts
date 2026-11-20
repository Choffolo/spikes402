/**
 * Runs in the page's own world (world: MAIN) and exposes window.webln.
 * It never sees the mnemonic. Calls go to the isolated content script, then
 * to the extension background.
 */
(() => {
  const SOURCE = "spikes402-webln";
  const REPLY = "spikes402-webln-reply";
  let enabled = false;
  let seq = 0;
  const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();

  window.addEventListener("message", (ev: MessageEvent) => {
    const d = ev.data;
    if (ev.source !== window || !d || d.source !== REPLY || typeof d.id !== "number") return;
    const w = pending.get(d.id);
    if (!w) return;
    pending.delete(d.id);
    if (d.error) w.reject(new Error(String(d.error)));
    else w.resolve(d.result);
  });

  function call(method: string, extra?: Record<string, unknown>): Promise<unknown> {
    const id = ++seq;
    return new Promise((resolve, reject) => {
      pending.set(id, { resolve, reject });
      window.postMessage({ source: SOURCE, id, method, origin: location.origin, ...extra }, "*");
      setTimeout(() => {
        if (pending.has(id)) {
          pending.delete(id);
          reject(new Error("The Spikes402 wallet did not answer in time."));
        }
      }, 5 * 60 * 1000);
    });
  }

  const webln = {
    get enabled() {
      return enabled;
    },
    async enable() {
      await call("enable");
      enabled = true;
    },
    async getInfo() {
      if (!enabled) throw new Error("Call webln.enable() first.");
      return call("getInfo");
    },
    async sendPayment(invoice: string) {
      if (!enabled) throw new Error("Call webln.enable() first.");
      return call("sendPayment", { invoice });
    },
  };

  Object.defineProperty(window, "webln", {
    configurable: true,
    enumerable: true,
    get() {
      return webln;
    },
  });
})();
