import type { Network } from "./types";

/** SDK network identifiers are uppercase. */
export function toSparkNetwork(network: Network): "MAINNET" | "REGTEST" {
  return network === "mainnet" ? "MAINNET" : "REGTEST";
}

/**
 * Hosts the official Spark SDK talks to (v0.16.0 defaults). Listed in the
 * manifest as host_permissions and connect-src. Do not load code from them;
 * they are only payment infrastructure (HTTPS / gRPC-web).
 */
export const SPARK_HOSTS = [
  "https://api.lightspark.com", // Spark Service Provider (Lightning), mainnet and regtest
  "wss://api.lightspark.com", // SSP GraphQL subscriptions
  "https://0.spark.lightspark.com", // Spark operator
  "https://spark-operator.breez.technology", // Spark operator (not the Breez SDK)
  "https://2.spark.flashnet.xyz", // Spark operator
  "https://mempool.space", // mainnet electrs / chain lookup
  "https://regtest-mempool.us-west-2.sparkinfra.net", // regtest electrs
] as const;

export function lightningHint(network: Network): { ready: boolean; hint: string } {
  if (network !== "mainnet") {
    return {
      ready: true,
      hint: "Network: Spark regtest (no real money). Invoice creation works; routing a real payment may still fail. Use mainnet with tiny amounts for Phoenix top-ups and 402 payments.",
    };
  }
  return { ready: true, hint: "Network: Bitcoin mainnet. Use only tiny amounts; this is unreviewed prototype software." };
}

/** Cap on Lightning routing + SSP fees we will accept for a given invoice amount. */
export function maxFeeSatsFor(amountSats: number): number {
  if (!Number.isFinite(amountSats) || amountSats < 0) return 5;
  return Math.max(5, Math.ceil(amountSats * 0.1));
}

export function looksLikeSparkAddress(s: string): boolean {
  return /^(spark|sprt|sp|tsp)1[a-z0-9]+$/i.test(s.trim());
}

const FAILED_SEND = new Set([
  "USER_TRANSFER_VALIDATION_FAILED",
  "LIGHTNING_PAYMENT_FAILED",
  "PREIMAGE_PROVIDING_FAILED",
  "TRANSFER_FAILED",
  "PENDING_USER_SWAP_RETURN",
  "USER_SWAP_RETURNED",
  "USER_SWAP_RETURN_FAILED",
]);

export function sendRequestFailed(status: string | undefined): boolean {
  return !!status && FAILED_SEND.has(status);
}

export function sendRequestHasPreimage(req: { paymentPreimage?: string } | null | undefined): string | undefined {
  const p = req?.paymentPreimage?.trim();
  return p ? p : undefined;
}
