import type { BudgetSettings, SpendState } from "./budget";
import type { EncryptedVault } from "./crypto";

export type Network = "regtest" | "mainnet";

export type Settings = BudgetSettings & {
  network: Network;
};

export const defaultSettings = (): Settings => ({
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  siteAllowances: {},
  network: "mainnet",
});

export type PaymentRecord = {
  time: string; // ISO
  origin: string;
  amountSats: number;
  destination: string; // invoice or address, may be truncated when stored
  result: "sent" | "failed" | "refused" | "pending";
  detail: string;
};

export type PendingPayment = {
  id: string;
  origin: string;
  invoice: string;
  amountSats: number;
  costSats: number;
  reason: string;
  createdAt: number;
  tabId?: number;
};

/**
 * A payment that was booked into the budget before it was sent. Its full
 * cost (amount plus the fee cap) is already counted in `spend` until the
 * payment is confirmed settled (cost becomes the real one) or confirmed
 * failed (cost is released). "uncertain" means we do not know yet.
 */
export type Reservation = {
  id: string;
  origin: string;
  day: string; // the budget day it was counted in
  amountSats: number;
  feeSats: number; // fee cap that was counted and given to the wallet
  invoice: string;
  state: "in_flight" | "uncertain";
  requestId?: string; // Spark Lightning send request id, once known
  time: string; // ISO
};

export type StoredState = {
  vault?: EncryptedVault;
  settings: Settings;
  spend: SpendState;
  history: PaymentRecord[];
  /** Payment confirmations waiting for the user, oldest first. */
  prompts: PendingPayment[];
  reservations: Reservation[];
};

export type WalletInfo = {
  unlocked: boolean;
  hasVault: boolean;
  network: Network;
  balanceSats?: number;
  pubkey?: string;
  lightningReady: boolean;
  lightningHint: string;
};
