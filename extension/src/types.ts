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
  result: "sent" | "failed" | "refused";
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

export type StoredState = {
  vault?: EncryptedVault;
  settings: Settings;
  spend: SpendState;
  history: PaymentRecord[];
  pending?: PendingPayment;
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
