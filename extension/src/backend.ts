import type { Network } from "./types";

/** The wallet implementation the rest of the extension talks to. */
export type WalletBackend = {
  connect(opts: { mnemonic: string; network: Network }): Promise<void>;
  disconnect(): Promise<void>;
  getInfo(): Promise<{ balanceSats: number; pubkey: string }>;
  /** Create a BOLT11 invoice for the given amount. */
  receiveLightning(amountSats: number): Promise<{ invoice: string; feeSats: number }>;
  /**
   * Pay a BOLT11 invoice or a Spark address.
   * amountSats is required when the destination has no amount of its own.
   */
  send(destination: string, amountSats?: number): Promise<{ preimage?: string; detail: string }>;
};

export class WalletError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WalletError";
  }
}

export { lightningHint } from "./spark-util";
