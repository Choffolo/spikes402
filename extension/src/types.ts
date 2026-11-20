import type { BudgetSettings, SpendState } from "./budget";
import type { EncryptedVault } from "./crypto";

export type Network = "regtest" | "mainnet";

export type Settings = BudgetSettings & {
  network: Network;
  /** Breez API key, required for Lightning on mainnet. Never committed. */
  breezApiKey: string;
};

export const defaultSettings = (): Settings => ({
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  autoPay: true,
  network: "regtest",
  breezApiKey: "",
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
  reason: string;
  createdAt: number;
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
  hasApiKey: boolean;
  balanceSats?: number;
  pubkey?: string;
  lightningReady: boolean;
  lightningHint: string;
};
