import { SparkWallet } from "@buildonspark/spark-sdk";
import type { WalletBackend } from "./backend";
import { WalletError } from "./backend";
import { assertPreimageMatchesInvoice, isBolt11 } from "./invoice";
import {
  feeSatsFromSendRequest,
  lightningHint,
  looksLikeSparkAddress,
  maxFeeSatsFor,
  sendRequestFailed,
  sendRequestHasPreimage,
  toSparkNetwork,
} from "./spark-util";

/**
 * Official Spark SDK adapter. Keys stay in this page (the offscreen / host
 * document). The SDK is bundled into the extension; no remote script or WASM
 * is fetched. Lightning invoices go through the default public SSP (no API key).
 */
const PREIMAGE_POLL_MS = 1000;
const PREIMAGE_POLL_ATTEMPTS = 60;

let wallet: SparkWallet | undefined;
let lastNetwork: "regtest" | "mainnet" = "mainnet";

export const sparkBackend: WalletBackend = {
  async connect({ mnemonic, network }) {
    lastNetwork = network;
    if (wallet) {
      try {
        await wallet.cleanup();
      } catch {
        /* already gone */
      }
      wallet = undefined;
    }
    const result = await SparkWallet.initialize({
      mnemonicOrSeed: mnemonic,
      options: { network: toSparkNetwork(network) },
    });
    wallet = result.wallet;
  },

  async disconnect() {
    if (!wallet) return;
    try {
      await wallet.cleanup();
    } finally {
      wallet = undefined;
    }
  },

  async getInfo() {
    const w = need();
    const [balance, pubkey] = await Promise.all([w.getBalance(), w.getIdentityPublicKey()]);
    const sats = Number(balance.satsBalance?.available ?? balance.balance);
    return { balanceSats: Number.isFinite(sats) ? sats : 0, pubkey };
  },

  async receiveLightning(amountSats) {
    const hint = lightningHint(lastNetwork);
    if (!hint.ready) throw new WalletError(hint.hint);
    if (!Number.isInteger(amountSats) || amountSats < 1) {
      throw new WalletError("Enter an amount of at least 1 sat.");
    }
    const request = await need().createLightningInvoice({
      amountSats,
      memo: "Spikes402 wallet top-up",
      expirySeconds: 3600,
    });
    const invoice = request.invoice?.encodedInvoice;
    if (!invoice) throw new WalletError("The Spark SDK did not return a Lightning invoice.");
    return { invoice, feeSats: 0 };
  },

  async send(destination, amountSats) {
    const w = need();
    const dest = destination.trim();
    if (!dest) throw new WalletError("Enter an invoice or address.");

    if (isBolt11(dest)) {
      const hint = lightningHint(lastNetwork);
      if (!hint.ready) throw new WalletError(hint.hint);
      const amount = amountSats !== undefined && amountSats > 0 ? amountSats : undefined;
      const payment = await w.payLightningInvoice({
        invoice: dest,
        maxFeeSats: maxFeeSatsFor(amount ?? 10),
        ...(amount !== undefined ? { amountSatsToSend: amount } : {}),
      });
      if (!payment || !("id" in payment) || !payment.id) {
        throw new WalletError("The payment did not return a Lightning send request (no preimage).");
      }
      const settled = await waitForPreimage(w, payment.id);
      await assertPreimageMatchesInvoice(dest, settled.preimage);
      return {
        preimage: settled.preimage,
        detail: `preimage ${settled.preimage}`,
        feeSats: settled.feeSats ?? maxFeeSatsFor(amount ?? 10),
      };
    }

    if (looksLikeSparkAddress(dest)) {
      if (!amountSats || amountSats < 1) {
        throw new WalletError("Sending to a Spark address needs an amount in sats.");
      }
      const transfer = await w.transfer({
        receiverSparkAddress: dest,
        amountSats,
      });
      const id = transfer && "id" in transfer ? String(transfer.id) : "";
      return { detail: id ? `Spark transfer ${id}` : "sent" };
    }

    throw new WalletError("Enter a Lightning invoice (lnbc…) or a Spark address (sp1… / spark1…).");
  },
};

function need(): SparkWallet {
  if (!wallet) throw new WalletError("The wallet is not unlocked.");
  return wallet;
}

async function waitForPreimage(
  w: SparkWallet,
  id: string,
): Promise<{ preimage: string; feeSats?: number }> {
  for (let i = 0; i < PREIMAGE_POLL_ATTEMPTS; i++) {
    const req = await w.getLightningSendRequest(id);
    const preimage = sendRequestHasPreimage(req);
    if (preimage) return { preimage, feeSats: feeSatsFromSendRequest(req) };
    if (sendRequestFailed(req?.status)) {
      throw new WalletError(`Lightning payment failed (${req?.status ?? "unknown"}).`);
    }
    await new Promise((r) => setTimeout(r, PREIMAGE_POLL_MS));
  }
  throw new WalletError("Timed out waiting for the Lightning payment preimage.");
}
