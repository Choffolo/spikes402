import { applyTabBadge, type TabPrice } from "./badge";
import {
  adjustSpend,
  decidePayment,
  paymentCost,
  recordSpend,
  spendForToday,
  todayLocal,
  withSiteAllowance,
  type ConfirmChoice,
  type Decision,
} from "./budget";
import { encryptMnemonic, generateMnemonic, isValidMnemonic, normalizeMnemonic } from "./crypto";
import { InvoiceError, isBolt11, parseBolt11AmountSats } from "./invoice";
import { lightningHint } from "./backend";
import { isExtensionPage, pageOrigin, sanitizeSettings } from "./sender";
import { maxFeeSatsFor } from "./spark-util";
import {
  appendHistory,
  loadState,
  pushHistory,
  saveSettings,
  saveVault,
  truncateDestination,
  updateState,
} from "./storage";
import type { PaymentRecord, PendingPayment, Reservation, Settings, WalletInfo } from "./types";

const CONFIRM_TTL_MS = 5 * 60 * 1000;
const MAX_PROMPTS = 10;
const RECONCILE_EVERY_MS = 30 * 1000;
const waiters = new Map<string, { resolve: (choice: ConfirmChoice) => void }>();
const tabPrices = new Map<number, TabPrice>();

// A restarted worker has lost the callbacks of every waiting prompt and of
// every payment that was being sent: drop the prompts, and treat the
// payments as unconfirmed (they stay counted in the budget).
void updateState((s) => {
  s.prompts = [];
  s.reservations = s.reservations.map((r) => (r.state === "in_flight" ? { ...r, state: "uncertain" } : r));
}).catch(() => undefined);

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

async function snapshot(): Promise<{
  info: WalletInfo;
  settings: Settings;
  history: PaymentRecord[];
  pending?: PendingPayment;
  pendingCount: number;
}> {
  void reconcile();
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
  return { info, settings: s.settings, history: [...s.history].reverse(), pending: s.prompts[0], pendingCount: s.prompts.length };
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
  void reconcile();
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
  // One fee rule: the amount we count in every cap is the amount we allow the wallet to spend.
  const feeSats = amountSats === null ? 0 : maxFeeSatsFor(amountSats);

  let reserved = await reserve({ origin, amountSats, feeSats, invoice, force: false });
  if (reserved.action === "refuse") {
    await log(origin, amountSats ?? 0, invoice, "refused", reserved.reason);
    throw new Error(reserved.reason);
  }
  if (reserved.action === "confirm") {
    const choice = await askUser({
      origin,
      invoice,
      amountSats: amountSats ?? 0,
      costSats: paymentCost(amountSats ?? 0, feeSats),
      reason: reserved.reason,
      tabId: sender.tab?.id,
    });
    if (choice === "refuse") {
      await log(origin, amountSats ?? 0, invoice, "refused", "You declined the payment.");
      throw new Error("Payment declined.");
    }
    reserved = await reserve({ origin, amountSats, feeSats, invoice, force: true });
  }
  if (reserved.action !== "reserved") throw new Error("The payment could not be reserved.");
  const reservation = reserved.reservation;

  // The budget is already booked, so the wallet call runs outside any lock:
  // a slow or hostile invoice cannot hold up other payments.
  try {
    const res = await walletCall<{ preimage?: string; detail: string; feeSats?: number }>("send", {
      destination: invoice,
      maxFeeSats: feeSats,
    });
    if (!res.preimage) throw new WalletCallError("The wallet paid, but did not return a preimage.", true);
    await finishReservation(reservation.id, { settled: true, feeSats: res.feeSats }, res.detail);
    markPaid(sender.tab?.id, amountSats ?? 0);
    return { preimage: res.preimage };
  } catch (e) {
    const detail = e instanceof Error ? e.message : String(e);
    if (e instanceof WalletCallError && e.uncertain) {
      await markUncertain(reservation.id, e.requestId, detail);
      scheduleReconcile();
      throw new Error(
        "The payment has not been confirmed yet. It may still go through, so it stays counted in your budget until it settles or fails. Do not pay this invoice again.",
      );
    }
    await finishReservation(reservation.id, { settled: false }, detail);
    throw e;
  }
}

type Reserved = Decision | { action: "reserved"; reservation: Reservation };

/**
 * Check the caps and book the full cost (amount plus fee cap) in one atomic
 * step. After a user confirmation (force) the caps are not asked again.
 */
function reserve(p: {
  origin: string;
  amountSats: number | null;
  feeSats: number;
  invoice: string;
  force: boolean;
}): Promise<Reserved> {
  return updateState((s): Reserved => {
    const today = todayLocal();
    const spend = spendForToday(s.spend, today);
    const d = decidePayment({
      amountSats: p.amountSats,
      origin: p.origin,
      settings: s.settings,
      spend,
      today,
      feeSats: p.feeSats,
    });
    if (d.action === "refuse" || (d.action === "confirm" && !p.force)) return d;
    const cost = paymentCost(p.amountSats ?? 0, p.feeSats);
    const reservation: Reservation = {
      id: crypto.randomUUID(),
      origin: p.origin,
      day: today,
      amountSats: p.amountSats ?? 0,
      feeSats: p.feeSats,
      invoice: p.invoice,
      state: "in_flight",
      time: new Date().toISOString(),
    };
    s.spend = recordSpend(spend, today, p.origin, cost);
    // A payment without a request id cannot be looked up; its day is over, so is its budget.
    s.reservations = [...s.reservations.filter((r) => r.requestId || r.day === today), reservation];
    return { action: "reserved", reservation };
  });
}

/** Settle a reservation: count the real cost, or release it on a confirmed failure. */
function finishReservation(
  id: string,
  outcome: { settled: true; feeSats?: number } | { settled: false },
  detail: string,
): Promise<void> {
  return updateState((s) => {
    const r = s.reservations.find((x) => x.id === id);
    if (!r) return;
    s.reservations = s.reservations.filter((x) => x.id !== id);
    const reserved = paymentCost(r.amountSats, r.feeSats);
    const actual = outcome.settled ? paymentCost(r.amountSats, outcome.feeSats ?? r.feeSats) : 0;
    s.spend = adjustSpend(spendForToday(s.spend, todayLocal()), r.day, r.origin, actual - reserved);
    pushHistory(s, {
      time: new Date().toISOString(),
      origin: r.origin,
      amountSats: outcome.settled ? actual : r.amountSats,
      destination: truncateDestination(r.invoice),
      result: outcome.settled ? "sent" : "failed",
      detail,
    });
  });
}

function markUncertain(id: string, requestId: string | undefined, detail: string): Promise<void> {
  return updateState((s) => {
    const r = s.reservations.find((x) => x.id === id);
    if (!r) return;
    r.state = "uncertain";
    if (requestId) r.requestId = requestId;
    pushHistory(s, {
      time: new Date().toISOString(),
      origin: r.origin,
      amountSats: paymentCost(r.amountSats, r.feeSats),
      destination: truncateDestination(r.invoice),
      result: "pending",
      detail: `${detail} Not confirmed; still counted in the budget.`,
    });
  });
}

let reconciling = false;
let reconcileTimer: ReturnType<typeof setTimeout> | undefined;

/** Retry the lookup of unconfirmed payments until each is settled or failed. */
function scheduleReconcile(): void {
  if (reconcileTimer !== undefined) return;
  reconcileTimer = setTimeout(() => {
    reconcileTimer = undefined;
    void reconcile();
  }, RECONCILE_EVERY_MS);
}

/**
 * Ask the wallet about every unconfirmed payment that has a request id. Only
 * a confirmed answer changes the budget: settled counts the real cost,
 * failed releases it, pending leaves it as it is.
 */
async function reconcile(): Promise<void> {
  if (reconciling) return;
  reconciling = true;
  try {
    const todo = (await loadState()).reservations.filter((r) => r.state === "uncertain" && r.requestId);
    let unresolved = false;
    for (const r of todo) {
      try {
        const res = await walletCall<{ state: "settled" | "failed" | "pending"; feeSats?: number }>("lookupSend", {
          requestId: r.requestId,
          invoice: r.invoice,
        });
        if (res.state === "settled") {
          await finishReservation(r.id, { settled: true, feeSats: res.feeSats }, "Confirmed later: the payment settled.");
        } else if (res.state === "failed") {
          await finishReservation(r.id, { settled: false }, "Confirmed later: the payment failed; the amount is free again.");
        } else {
          unresolved = true;
        }
      } catch {
        unresolved = true; // wallet locked or busy: try again later
      }
    }
    if (unresolved) scheduleReconcile();
  } finally {
    reconciling = false;
  }
}

/**
 * Queue a confirmation for the user. Each prompt has its own id and timer;
 * an expired or answered prompt is gone and cannot be answered again.
 */
function askUser(p: Omit<PendingPayment, "id" | "createdAt">): Promise<ConfirmChoice> {
  const id = crypto.randomUUID();
  const pending: PendingPayment = { ...p, id, createdAt: Date.now() };
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      if (!waiters.delete(id)) return; // already answered
      void updateState((s) => {
        s.prompts = s.prompts.filter((x) => x.id !== id);
      });
      markConfirm(pending.tabId, false);
      resolve("refuse");
    }, CONFIRM_TTL_MS);
    waiters.set(id, {
      resolve: (choice) => {
        clearTimeout(timer);
        resolve(choice);
      },
    });
    updateState((s) => {
      if (s.prompts.length >= MAX_PROMPTS) return false;
      s.prompts = [...s.prompts, pending];
      return true;
    }).then(
      (queued) => {
        if (!queued) {
          clearTimeout(timer);
          waiters.delete(id);
          resolve("refuse");
          return;
        }
        markConfirm(pending.tabId, true);
        void openPopup(pending);
      },
      () => {
        clearTimeout(timer);
        waiters.delete(id);
        resolve("refuse");
      },
    );
  });
}

async function resolvePending(id: string, choice: ConfirmChoice): Promise<{ ok: true }> {
  let answered: { pending: PendingPayment; waiter: { resolve: (c: ConfirmChoice) => void } } | undefined;
  await updateState((s) => {
    const pending = s.prompts.find((x) => x.id === id);
    const waiter = waiters.get(id);
    // No waiter means it expired or the worker restarted: drop it and store nothing.
    s.prompts = s.prompts.filter((x) => x.id !== id);
    if (!pending || !waiter) return;
    waiters.delete(id); // same synchronous step as the check: the timer cannot slip in
    if (choice === "always") s.settings = { ...s.settings, ...withSiteAllowance(s.settings, pending.origin, pending.costSats) };
    answered = { pending, waiter };
  });
  if (!answered) throw new Error("This confirmation has expired.");
  markConfirm(answered.pending.tabId, false);
  answered.waiter.resolve(choice);
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

/** A wallet call failed. `uncertain` means the payment may still have gone out. */
class WalletCallError extends Error {
  constructor(
    message: string,
    readonly uncertain = false,
    readonly requestId?: string,
  ) {
    super(message);
  }
}

// The message never reached the wallet host, so nothing was sent.
const NOT_DELIVERED = /receiving end does not exist|could not establish connection/i;

function walletCall<T = unknown>(type: string, extra?: Record<string, unknown>): Promise<T> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage({ target: "wallet", type, ...extra }, (res) => {
      if (chrome.runtime.lastError) {
        const m = chrome.runtime.lastError.message ?? "The wallet host did not answer.";
        reject(new WalletCallError(m, type === "send" && !NOT_DELIVERED.test(m)));
        return;
      }
      if (res && typeof res === "object" && "error" in res && res.error) {
        const r = res as { error: unknown; uncertain?: boolean; requestId?: string };
        reject(new WalletCallError(String(r.error), !!r.uncertain, r.requestId));
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
