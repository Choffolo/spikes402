/** Errors about a string that is not a usable BOLT11 invoice. */
export class InvoiceError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "InvoiceError";
  }
}

/**
 * Reads the amount encoded in a BOLT11 invoice, in satoshis, rounded up.
 * Returns null if the invoice is valid-looking but has no amount (amountless).
 * Does not fully verify the bech32 checksum; the wallet backend does that
 * before paying. Addresses (name@domain, Spark, on-chain) are not invoices.
 */
export function parseBolt11AmountSats(invoice: string): number | null {
  const raw = invoice.trim().toLowerCase();
  if (raw === "") throw new InvoiceError("Invoice is empty");
  if (!raw.startsWith("ln")) {
    throw new InvoiceError("Not a Lightning invoice");
  }
  const m = raw.match(/^ln[a-z]+(?:(\d+)([munp])?)?1/);
  if (!m) throw new InvoiceError("Invoice is malformed");
  if (m[1] === undefined) return null;
  return unitsToSats(BigInt(m[1]), m[2]);
}

/** True if s looks like a BOLT11 invoice (lnbc…, lntb…, lnbcrt…). */
export function isBolt11(s: string): boolean {
  return /^ln[a-z]+(?:\d+[munp]?)?1[0-9a-z]+$/i.test(s.trim());
}

function unitsToSats(amount: bigint, multiplier: string | undefined): number {
  // 1 bitcoin = 100_000_000 sat. Multipliers are the usual BOLT11 ones.
  let sats: bigint;
  switch (multiplier) {
    case undefined:
      sats = amount * 100_000_000n;
      break;
    case "m":
      sats = amount * 100_000n;
      break;
    case "u":
      sats = amount * 100n;
      break;
    case "n":
      sats = ceilDiv(amount, 10n); // 1 nano-BTC = 0.1 sat
      break;
    case "p":
      sats = ceilDiv(amount, 10_000n);
      break;
    default:
      throw new InvoiceError("Unknown amount multiplier");
  }
  if (sats <= 0n) throw new InvoiceError("Invoice amount is zero");
  if (sats > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new InvoiceError("Invoice amount is too large");
  }
  return Number(sats);
}

function ceilDiv(a: bigint, b: bigint): bigint {
  return (a + b - 1n) / b;
}
