import type { WalletBackend } from "./backend";
import { WalletError, lightningHint } from "./backend";
import type { Network } from "./types";

/**
 * Breez Spark adapter. The WASM module is loaded from the extension package
 * (vendor/breez-sdk), never from the network.
 *
 * Lightning invoices (the Spikes402 path, and topping up from Phoenix) only
 * work on mainnet with a Breez API key. Regtest needs no key and is the
 * default for development, but has no Lightning.
 */
// The vendor copy is plain JS; we describe the few calls we use.
type BreezSdk = {
  getInfo(req: { ensureSynced?: boolean }): Promise<{ balanceSats: number; identityPubkey: string }>;
  receivePayment(req: {
    paymentMethod: {
      type: "bolt11Invoice";
      description: string;
      amountSats?: number;
      expirySecs?: number;
    };
  }): Promise<{ paymentRequest: string; fee: bigint }>;
  prepareSendPayment(req: {
    paymentRequest: { type: "input"; input: string };
    amount?: bigint;
  }): Promise<unknown>;
  sendPayment(req: { prepareResponse: unknown }): Promise<{
    payment: {
      details?: { type: string; htlcDetails?: { preimage?: string } };
    };
  }>;
  disconnect(): Promise<void>;
};

type BreezModule = {
  default: () => Promise<unknown>;
  defaultConfig: (network: Network) => { apiKey?: string; network: Network };
  connect: (req: {
    config: { apiKey?: string; network: Network };
    seed: { type: "mnemonic"; mnemonic: string };
    storageDir: string;
  }) => Promise<BreezSdk>;
};

let sdk: BreezSdk | undefined;

async function loadModule(): Promise<BreezModule> {
  const url = new URL("./vendor/breez-sdk/index.js", self.location.href).href;
  return (await import(/* @vite-ignore */ url)) as BreezModule;
}

export const breezBackend: WalletBackend = {
  async connect({ mnemonic, network, apiKey }) {
    lastNetwork = network;
    lastKey = apiKey;
    const mod = await loadModule();
    await mod.default();
    const config = mod.defaultConfig(network);
    if (apiKey.trim()) config.apiKey = apiKey.trim();
    if (sdk) {
      try {
        await sdk.disconnect();
      } catch {
        /* already gone */
      }
    }
    sdk = await mod.connect({
      config,
      seed: { type: "mnemonic", mnemonic },
      storageDir: "spikes402-wallet",
    });
  },

  async disconnect() {
    if (!sdk) return;
    try {
      await sdk.disconnect();
    } finally {
      sdk = undefined;
    }
  },

  async getInfo() {
    const s = need();
    const info = await s.getInfo({ ensureSynced: false });
    return { balanceSats: info.balanceSats, pubkey: info.identityPubkey };
  },

  async receiveLightning(amountSats) {
    const s = need();
    const hint = currentHint();
    if (!hint.ready) throw new WalletError(hint.hint);
    const res = await s.receivePayment({
      paymentMethod: {
        type: "bolt11Invoice",
        description: "Spikes402 wallet top-up",
        amountSats,
        expirySecs: 3600,
      },
    });
    return { invoice: res.paymentRequest, feeSats: Number(res.fee) };
  },

  async send(destination, amountSats) {
    const s = need();
    const hint = currentHint();
    // Paying a Lightning invoice on regtest will fail at the SDK; we say so first
    // when the destination is clearly an invoice or we have no key on mainnet.
    if (!hint.ready && destination.trim().toLowerCase().startsWith("ln")) {
      throw new WalletError(hint.hint);
    }
    const prepareResponse = await s.prepareSendPayment({
      paymentRequest: { type: "input", input: destination.trim() },
      amount: amountSats !== undefined ? BigInt(amountSats) : undefined,
    });
    const res = await s.sendPayment({ prepareResponse });
    const preimage = res.payment.details?.htlcDetails?.preimage;
    return { preimage, detail: preimage ? `preimage ${preimage}` : "sent" };
  },
};

function need(): BreezSdk {
  if (!sdk) throw new WalletError("The wallet is not unlocked.");
  return sdk;
}

// Filled by the host when connect() is called, so receive/send can explain
// a missing key without threading settings through every call.
let lastNetwork: Network = "regtest";
let lastKey = "";

export function rememberNetwork(network: Network, apiKey: string) {
  lastNetwork = network;
  lastKey = apiKey;
}

function currentHint() {
  return lightningHint(lastNetwork, lastKey);
}
