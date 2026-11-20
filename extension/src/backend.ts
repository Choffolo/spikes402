import type { Network } from "./types";

/** The wallet implementation the rest of the extension talks to. */
export type WalletBackend = {
  connect(opts: { mnemonic: string; network: Network; apiKey: string }): Promise<void>;
  disconnect(): Promise<void>;
  getInfo(): Promise<{ balanceSats: number; pubkey: string }>;
  /** Create a BOLT11 invoice for the given amount. */
  receiveLightning(amountSats: number): Promise<{ invoice: string; feeSats: number }>;
  /**
   * Pay a BOLT11 invoice, Lightning address, or Spark/Bitcoin address.
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

export function lightningHint(network: Network, apiKey: string): { ready: boolean; hint: string } {
  if (network !== "mainnet") {
    return {
      ready: false,
      hint: "Lightning send and receive need mainnet: the Breez Spark regtest network has no Lightning. Set the network and a Breez API key in Settings.",
    };
  }
  if (!apiKey.trim()) {
    return {
      ready: false,
      hint: "Lightning on mainnet needs a Breez API key. See the README for how to request one (we do not sign up for you).",
    };
  }
  return { ready: true, hint: "Lightning is configured (mainnet). Use tiny amounts." };
}
