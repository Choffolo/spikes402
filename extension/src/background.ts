import { applyTabBadge, type TabPrice } from "./badge";
import {
  decidePayment,
  paymentCost,
  recordSpend,
  spendForToday,
  todayLocal,
  withSiteAllowance,
  type ConfirmChoice,
} from "./budget";
import { encryptMnemonic, generateMnemonic, isValidMnemonic, normalizeMnemonic } from "./crypto";
import { InvoiceError, isBolt11, parseBolt11AmountSats } from "./invoice";
import { lightningHint } from "./backend";
import { isExtensionPage, pageOrigin, sanitizeSettings } from "./sender";
import { maxFeeSatsFor } from "./spark-util";
import {
  appendHistory,
  loadState,
  saveSettings,
  saveSpend,
  saveVault,
  setPending,
  truncateDestination,
} from "./storage";
import type { PaymentRecord, PendingPayment, Settings, WalletInfo } from "./types";

const CONFIRM_TTL_MS = 5 * 60 * 1000;
const waiters = new Map<string, { resolve: (choice: ConfirmChoice) => void }>();
const tabPrices = new Map<number, TabPrice>();

/** Serializes budget checks and payments so two pages cannot double-spend a cap. */
let budgetTail: Promise<void> = Promise.resolve();
function withBudgetLock<T>(fn: () => Promise<T>): Promise<T> {
  const run = budgetTail.then(fn, fn);
  budgetTail = run.then(
    () => undefined,
    () => undefined,
  );
  return run;
}

type PopupMsg =
  | { type: "getState" }
  | { type: "createWallet"; password: string }
  | { type: "restoreWallet"; mnemonic: string; password: string }
  | { type: "unlock"; password: string }
  | { type: "lock" }
  | { type: "saveSettings"; settings: Partial<Settings> }
  | { type: "receive"; amountSats: number }
  | { type: "send"; destination: string; amountSats?: number }
  | { type: "confirmPayment"; id: string; choice: ConfirmChoice };

type PageMsg = { type: "webln"; method: "enable" | "getInfo" | "sendPayment"; origin: string; invoice?: string };
type PagePriceMsg = { type: "pagePrice"; amountSats: number | null };

chrome.runtime.onInstalled.addListener(() => {
  void ensureWalletHost();
});

chrome.runtime.onStartup?.addListener(() => {
  void ensureWalletHost();
});

chrome.tabs.onRemoved.addListener((tabId) => {
  tabPrices.delete(tabId);
});

chrome.runtime.onMessage.addListener((msg: PopupMsg | PageMsg | PagePriceMsg, sender, sendResponse) => {
  if (!msg || typeof msg !== "object" || !("type" in msg)) return;
  if ((msg as { target?: string }).target === "wallet") return; // for the wallet host
  handle(msg, sender)
    .then(sendResponse)
    .catch((e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
  return true;
});

async function handle(msg: PopupMsg | PageMsg | PagePriceMsg, sender: chrome.runtime.MessageSender): Promise<unknown> {
  if (msg.type === "pagePrice") {
    handlePagePrice(msg, sender);
    return { ok: true };
  }
  if (msg.type !== "webln" && !isExtensionPage(sender, chrome.runtime.getURL(""))) {
    throw new Error("This action is only available from the extension popup.");
  }
  switch (msg.type) {
    case "getState":
      return snapshot();
    case "createWallet":
      return createWallet(msg.password);
    case "restoreWallet":
      return restoreWallet(msg.mnemonic, msg.password);
    case "unlock":
      return unlock(msg.password);
    case "lock":
      return lock();
    case "saveSettings":
      await saveSettings(sanitizeSettings(msg.settings as unknown as Record<string, unknown>));
      return snapshot();
    case "receive":
      return receiveInvoice(msg.amountSats);
    case "send":
      return sendFromPopup(msg.destination, msg.amountSats);
    case "confirmPayment":
      return resolvePending(msg.id, msg.choice);
    case "webln":
      return handleWebln(msg, sender);
    default:
      throw new Error("Unknown message");
  }
}

async function snapshot(): Promise<{ info: WalletInfo; settings: Settings; history: PaymentRecord[]; pending?: PendingPayment }> {
  const s = await loadState();
  const host = await walletCall<{ unlocked?: boolean }>("status").catch(() => ({ unlocked: false }));
  const unlocked = !!host.unlocked;
  const hint = lightningHint(s.settings.network);
  const info: WalletInfo = {
    unlocked,
    hasVault: !!s.vault,
    network: s.settings.network,
    lightningReady: hint.ready,
    lightningHint: hint.hint,
  };
  if (unlocked) {
    try {
      const w = await walletCall<{ balanceSats: number; pubkey: string }>("getInfo");
      info.balanceSats = w.balanceSats;
    } catch (e) {
      info.lightningHint = e instanceof Error ? e.message : String(e);
    }
  }
  return { info, settings: s.settings, history: [...s.history].reverse(), pending: s.pending };
}

async function createWallet(password: string): Promise<{ mnemonic: string }> {
  const s = await loadState();
  if (s.vault) throw new Error("A wallet already exists in this browser. Unlock it, or remove the extension data first.");
  const mnemonic = generateMnemonic();
  await saveVault(await encryptMnemonic(mnemonic, password));
  await unlock(password);
  return { mnemonic };
}

async function restoreWallet(mnemonic: string, password: string): Promise<{ ok: true }> {
  const s = await loadState();
  if (s.vault) throw new Error("A wallet already exists in this browser. Unlock it, or remove the extension data first.");
  const words = normalizeMnemonic(mnemonic);
  if (!isValidMnemonic(words)) throw new Error("That is not a valid 12- or 24-word recovery phrase.");
  await saveVault(await encryptMnemonic(words, password));
  await unlock(password);
  return { ok: true };
}

async function unlock(password: string): Promise<{ ok: true }> {
  const s = await loadState();
  if (!s.vault) throw new Error("No wallet is stored yet.");
  await ensureWalletHost();
  // Decrypt only in the wallet host, so the service worker never holds the phrase.
  await walletCall("connect", {
    vault: s.vault,
    password,
    network: s.settings.network,
  });
  return { ok: true };
}

async function lock(): Promise<{ ok: true }> {
  await walletCall("disconnect").catch(() => undefined);
  return { ok: true };
}

async function receiveInvoice(amountSats: number): Promise<{ invoice: string; feeSats: number }> {
  if (!Number.isInteger(amountSats) || amountSats < 1) throw new Error("Enter an amount of at least 1 sat.");
  return walletCall("receive", { amountSats });
}

async function sendFromPopup(destination: string, amountSats?: number): Promise<{ detail: string }> {
  const dest = destination.trim();
  if (!dest) throw new Error("Enter an invoice or address.");
  let amount = amountSats;
  if (isBolt11(dest)) {
    const encoded = parseBolt11AmountSats(dest);
    if (encoded !== null) amount = encoded;
  }
  const res = await walletCall<{ preimage?: string; detail: string }>("send", { destination: dest, amountSats: amount });
  await appendHistory({
    time: new Date().toISOString(),
    origin: "popup",
    amountSats: amount ?? 0,
    destination: truncateDestination(dest),
    result: "sent",
    detail: res.detail,
  });
  return res;
}

async function handleWebln(msg: PageMsg, sender: chrome.runtime.MessageSender): Promise<unknown> {
  const origin = pageOrigin(sender);
  if (!origin) throw new Error("This payment request has no page origin.");

  const host = await walletCall<{ unlocked?: boolean }>("status").catch(() => ({ unlocked: false }));
  if (!host.unlocked) throw new Error("The Spikes402 wallet is locked. Open the extension and unlock it.");

  if (msg.method === "enable") return { enabled: true };
  if (msg.method === "getInfo") {
    return { node: { alias: "Spikes402" }, methods: ["sendPayment", "getInfo"] };
  }
  if (msg.method !== "sendPayment" || !msg.invoice) throw new Error("sendPayment needs an invoice.");
  const invoice = msg.invoice;

  let amountSats: number | null;
  try {
    amountSats = parseBolt11AmountSats(invoice);
  } catch (e) {
    const reason = e instanceof InvoiceError ? e.message : "Invoice could not be read.";
    await log(origin, 0, invoice, "refused", reason);
    throw new Error(reason);
  }
  const feeSats = amountSats === null ? 0 : maxFeeSatsFor(amountSats);

  let confirmReason: string | undefined;
  const first = await withBudgetLock(async () => decideWebln(origin, amountSats, feeSats));
  if (first.action === "refuse") {
    await log(origin, amountSats ?? 0, invoice, "refused", first.reason);
    throw new Error(first.reason);
  }
  if (first.action === "confirm") {
    confirmReason = first.reason;
    const choice = await askUser({
      origin,
      invoice,
      amountSats: amountSats ?? 0,
      costSats: paymentCost(amountSats ?? 0, feeSats),
      reason: first.reason,
      tabId: sender.tab?.id,
    });
    if (choice === "refuse") {
      await log(origin, amountSats ?? 0, invoice, "refused", "You declined the payment.");
      throw new Error("Payment declined.");
    }
  }

  return withBudgetLock(async () => {
    if (!confirmReason) {
      const again = await decideWebln(origin, amountSats, feeSats);
      if (again.action !== "pay") {
        await log(origin, amountSats ?? 0, invoice, "refused", again.reason);
        throw new Error(again.reason);
      }
    }
    const s = await loadState();
    const today = todayLocal();
    const spend = spendForToday(s.spend, today);
    try {
      const res = await walletCall<{ preimage?: string; detail: string; feeSats?: number }>("send", {
        destination: invoice,
      });
      if (!res.preimage) throw new Error("The wallet paid, but did not return a preimage.");
      const recorded = paymentCost(amountSats ?? 0, res.feeSats ?? feeSats);
      await saveSpend(recordSpend(spend, today, origin, recorded));
      await log(origin, recorded, invoice, "sent", res.detail);
      markPaid(sender.tab?.id, amountSats ?? recorded);
      return { preimage: res.preimage };
    } catch (e) {
      const detail = e instanceof Error ? e.message : String(e);
      await log(origin, amountSats ?? 0, invoice, "failed", detail);
      throw e;
    }
  });
}

async function decideWebln(origin: string, amountSats: number | null, feeSats: number) {
  const s = await loadState();
  const today = todayLocal();
  return decidePayment({
    amountSats,
    origin,
    settings: s.settings,
    spend: spendForToday(s.spend, today),
    today,
    feeSats,
  });
}

function askUser(p: Omit<PendingPayment, "id" | "createdAt">): Promise<ConfirmChoice> {
  const id = crypto.randomUUID();
  const pending: PendingPayment = { ...p, id, createdAt: Date.now() };
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      waiters.delete(id);
      void setPending(undefined);
      markConfirm(pending.tabId, false);
      resolve("refuse");
    }, CONFIRM_TTL_MS);
    waiters.set(id, {
      resolve: (choice) => {
        clearTimeout(timer);
        resolve(choice);
      },
    });
    void setPending(pending).then(() => {
      markConfirm(pending.tabId, true);
      void openPopup(pending);
    });
  });
}

async function resolvePending(id: string, choice: ConfirmChoice): Promise<{ ok: true }> {
  const s = await loadState();
  if (!s.pending || s.pending.id !== id) throw new Error("This confirmation has expired.");
  const pending = s.pending;
  if (choice === "always") {
    await saveSettings(withSiteAllowance(s.settings, pending.origin, pending.costSats));
  }
  await setPending(undefined);
  markConfirm(pending.tabId, false);
  waiters.get(id)?.resolve(choice);
  waiters.delete(id);
  return { ok: true };
}

function handlePagePrice(msg: PagePriceMsg, sender: chrome.runtime.MessageSender): void {
  const tabId = sender.tab?.id;
  if (tabId === undefined) return;
  const amount =
    typeof msg.amountSats === "number" && Number.isFinite(msg.amountSats) && msg.amountSats > 0
      ? Math.floor(msg.amountSats)
      : undefined;
  if (!amount) {
    tabPrices.delete(tabId);
    void applyTabBadge(tabId, undefined);
    return;
  }
  const prev = tabPrices.get(tabId);
  const paid = !!prev?.paid && prev.amountSats === amount;
  const state: TabPrice = { amountSats: amount, paid, needsConfirm: !!prev?.needsConfirm };
  tabPrices.set(tabId, state);
  void applyTabBadge(tabId, state);
}

function markPaid(tabId: number | undefined, amountSats: number): void {
  if (tabId === undefined) return;
  const prev = tabPrices.get(tabId);
  const state: TabPrice = {
    amountSats: prev?.amountSats ?? amountSats,
    paid: true,
    needsConfirm: false,
  };
  tabPrices.set(tabId, state);
  void applyTabBadge(tabId, state);
}

function markConfirm(tabId: number | undefined, needsConfirm: boolean): void {
  if (tabId === undefined) return;
  const prev = tabPrices.get(tabId);
  if (!prev) return;
  const state = { ...prev, needsConfirm };
  tabPrices.set(tabId, state);
  void applyTabBadge(tabId, state);
}

async function log(
  origin: string,
  amountSats: number,
  destination: string,
  result: PaymentRecord["result"],
  detail: string,
) {
  await appendHistory({
    time: new Date().toISOString(),
    origin,
    amountSats,
    destination: truncateDestination(destination),
    result,
    detail,
  });
}

async function openPopup(pending?: { origin: string; amountSats: number }) {
  try {
    if (chrome.action.openPopup) await chrome.action.openPopup();
  } catch {
    /* some Chrome views refuse this; the toolbar badge still shows the page price */
  }
  const summary = pending
    ? `Pay ${pending.amountSats} sats to ${pending.origin}? Open the extension to confirm or decline.`
    : "Open the extension to confirm or decline.";
  try {
    await chrome.notifications.create("spikes402-confirm", {
      type: "basic",
      iconUrl: "icons/icon-128.png",
      title: "Spikes402: confirm a payment",
      message: summary,
      priority: 2,
    });
  } catch {
    /* notifications permission may be missing on some browsers */
  }
}

function walletCall<T = unknown>(type: string, extra?: Record<string, unknown>): Promise<T> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage({ target: "wallet", type, ...extra }, (res) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (res && typeof res === "object" && "error" in res && res.error) {
        reject(new Error(String(res.error)));
        return;
      }
      resolve(res as T);
    });
  });
}

async function ensureWalletHost(): Promise<void> {
  const url = chrome.runtime.getURL("offscreen.html");
  if (chrome.offscreen) {
    const already = await chrome.runtime.getContexts?.({ contextTypes: ["OFFSCREEN_DOCUMENT"] });
    if (already && already.length > 0) return;
    await chrome.offscreen.createDocument({
      url: "offscreen.html",
      reasons: [chrome.offscreen.Reason.WORKERS],
      justification: "Run the local Spark wallet outside the short-lived service worker.",
    });
    return;
  }
  // Firefox has no offscreen documents. A small host window is the fallback;
  // see README.
  const wins = await chrome.windows.getAll({ populate: true });
  const open = wins.some((w) => w.tabs?.some((t) => t.url === url));
  if (open) return;
  await chrome.windows.create({ url, type: "popup", width: 360, height: 200, focused: false });
}
