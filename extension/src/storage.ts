import { defaultBudget, emptySpend, type SpendState } from "./budget";
import { defaultSettings, type PaymentRecord, type PendingPayment, type Settings, type StoredState } from "./types";
import type { EncryptedVault } from "./crypto";

const KEY = "spikes402";
const HISTORY_MAX = 50;

const fallback = (): StoredState => ({
  settings: defaultSettings(),
  spend: emptySpend("1970-01-01"),
  history: [],
});

export async function loadState(): Promise<StoredState> {
  const raw = await chrome.storage.local.get(KEY);
  const s = (raw[KEY] ?? fallback()) as StoredState;
  return {
    vault: s.vault,
    settings: { ...defaultSettings(), ...s.settings },
    spend: s.spend ?? emptySpend("1970-01-01"),
    history: Array.isArray(s.history) ? s.history : [],
    pending: s.pending,
  };
}

async function saveState(s: StoredState): Promise<void> {
  await chrome.storage.local.set({ [KEY]: s });
}

export async function saveVault(vault: EncryptedVault): Promise<void> {
  const s = await loadState();
  s.vault = vault;
  await saveState(s);
}

export async function saveSettings(patch: Partial<Settings>): Promise<Settings> {
  const s = await loadState();
  s.settings = { ...s.settings, ...patch };
  await saveState(s);
  return s.settings;
}

export async function saveSpend(spend: SpendState): Promise<void> {
  const s = await loadState();
  s.spend = spend;
  await saveState(s);
}

export async function appendHistory(rec: PaymentRecord): Promise<void> {
  const s = await loadState();
  s.history = [...s.history, rec].slice(-HISTORY_MAX);
  await saveState(s);
}

export async function setPending(pending: PendingPayment | undefined): Promise<void> {
  const s = await loadState();
  s.pending = pending;
  await saveState(s);
  await chrome.action.setBadgeText({ text: pending ? "!" : "" });
  await chrome.action.setBadgeBackgroundColor({ color: "#b45309" });
}

export function truncateDestination(s: string, n = 72): string {
  const t = s.trim();
  return t.length <= n ? t : t.slice(0, n) + "…";
}

export { defaultBudget };
