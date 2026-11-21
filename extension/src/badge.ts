/** Toolbar badge: the sats this tab's page costs to open. */

export type TabPrice = {
  amountSats: number;
  paid: boolean;
  needsConfirm: boolean;
};

const PAID = "#0f766e";
const CONFIRM = "#b45309";
const PRICE = "#44403c";

/** Short text for chrome.action.setBadgeText (about four characters). */
export function formatBadgeText(amountSats: number): string {
  if (!Number.isFinite(amountSats) || amountSats < 1) return "";
  if (amountSats < 10_000) return String(Math.floor(amountSats));
  if (amountSats < 1_000_000) return `${Math.floor(amountSats / 1000)}k`;
  return "1M+";
}

export function badgeColor(state: TabPrice): string {
  if (state.needsConfirm) return CONFIRM;
  if (state.paid) return PAID;
  return PRICE;
}

export function badgeTitle(state: TabPrice): string {
  const n = state.amountSats;
  if (state.paid) return `Paid ${n} sats to open this page.`;
  if (state.needsConfirm) return `This page costs ${n} sats to open. Confirm in the popup.`;
  return `This page costs ${n} sats to open.`;
}

export async function applyTabBadge(tabId: number, state: TabPrice | undefined): Promise<void> {
  if (tabId < 0) return;
  try {
    if (!state) {
      await chrome.action.setBadgeText({ tabId, text: "" });
      await chrome.action.setTitle({ tabId, title: "Spikes402 Wallet" });
      return;
    }
    await chrome.action.setBadgeText({ tabId, text: formatBadgeText(state.amountSats) });
    await chrome.action.setBadgeBackgroundColor({ tabId, color: badgeColor(state) });
    await chrome.action.setTitle({ tabId, title: badgeTitle(state) });
  } catch {
    /* tab may already be gone */
  }
}
