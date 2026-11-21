import { defaultBudget, emptySpend, type SpendState } from "./budget";
import { defaultSettings, type PaymentRecord, type PendingPayment, type Settings, type StoredState } from "./types";
import type { EncryptedVault } from "./crypto";

const KEY = "spikes402";
const HISTORY_MAX = 20;

const fallback = (): StoredState => ({
  settings: defaultSettings(),
  spend: emptySpend("1970-01-01"),
  history: [],
  prompts: [],
  reservations: [],
});

/** A plain read. For a change, use updateState so it cannot be lost. */
export async function loadState(): Promise<StoredState> {
  const raw = await chrome.storage.local.get(KEY);
  const s = (raw[KEY] ?? fallback()) as StoredState;
  return {
    vault: s.vault,
    backupPending: s.backupPending === true,
    settings: {
      ...defaultSettings(),
      ...s.settings,
      siteAllowances: { ...defaultSettings().siteAllowances, ...(s.settings?.siteAllowances ?? {}) },
    },
    spend: s.spend ?? emptySpend("1970-01-01"),
    history: Array.isArray(s.history) ? s.history : [],
    prompts: Array.isArray(s.prompts) ? s.prompts : [],
    reservations: Array.isArray(s.reservations) ? s.reservations : [],
  };
}

async function saveState(s: StoredState): Promise<void> {
  await chrome.storage.local.set({ [KEY]: s });
}

/**
 * Every change to the stored state goes through this one queue: read,
 * change, write, one at a time, so two changes can never overwrite each
 * other. Keep fn quick (no wallet or network calls). If fn throws, nothing
 * is written.
 */
let queueTail: Promise<unknown> = Promise.resolve();
export function updateState<T>(fn: (s: StoredState) => T | Promise<T>): Promise<T> {
  const run = queueTail.then(async () => {
    const s = await loadState();
    const out = await fn(s);
    await saveState(s);
    return out;
  });
  queueTail = run.then(
    () => undefined,
    () => undefined,
  );
  return run;
}

export function pushHistory(s: StoredState, rec: PaymentRecord): void {
  s.history = [...s.history, rec].slice(-HISTORY_MAX);
}

export async function saveVault(vault: EncryptedVault): Promise<void> {
  await updateState((s) => {
    s.vault = vault;
  });
}

export async function saveSettings(patch: Partial<Settings>): Promise<Settings> {
  return updateState((s) => {
    s.settings = { ...s.settings, ...patch };
    return s.settings;
  });
}

export async function saveSpend(spend: SpendState): Promise<void> {
  await updateState((s) => {
    s.spend = spend;
  });
}

export async function appendHistory(rec: PaymentRecord): Promise<void> {
  await updateState((s) => pushHistory(s, rec));
}

export type { PendingPayment };

export function truncateDestination(s: string, n = 72): string {
  const t = s.trim();
  return t.length <= n ? t : t.slice(0, n) + "…";
}

export { defaultBudget };
