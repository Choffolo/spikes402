import { describe, expect, it } from "vitest";
import {
  InvoiceError,
  assertPreimageMatchesInvoice,
  isBolt11,
  parseBolt11AmountSats,
  parseBolt11PaymentHash,
  preimageMatchesInvoice,
} from "./invoice";

describe("parseBolt11AmountSats", () => {
  it("reads the usual multipliers", () => {
    expect(parseBolt11AmountSats("lnbc20m1qqqq")).toBe(2_000_000);
    expect(parseBolt11AmountSats("lnbc1u1qqqq")).toBe(100);
    expect(parseBolt11AmountSats("lnbc100n1qqqq")).toBe(10);
    expect(parseBolt11AmountSats("lnbc10n1qqqq")).toBe(1);
    expect(parseBolt11AmountSats("lnbc2500u1qqqq")).toBe(250_000);
  });

  it("rounds fractional sats up (nano and pico)", () => {
    expect(parseBolt11AmountSats("lnbc15n1qqqq")).toBe(2); // 1.5 sat
    expect(parseBolt11AmountSats("lnbc1n1qqqq")).toBe(1); // 0.1 sat
    expect(parseBolt11AmountSats("lnbc1p1qqqq")).toBe(1);
  });

  it("treats a bare number as whole bitcoin", () => {
    expect(parseBolt11AmountSats("lnbc11qqqq")).toBe(100_000_000); // 1 BTC
  });

  it("returns null for amountless invoices", () => {
    expect(parseBolt11AmountSats("lnbc1qqqqqq")).toBeNull();
    expect(parseBolt11AmountSats("  LNBC1QQQQ  ")).toBeNull();
  });

  it("accepts testnet and regtest prefixes, and a lightning: prefix", () => {
    expect(parseBolt11AmountSats("lntb100n1qqqq")).toBe(10);
    expect(parseBolt11AmountSats("lnbcrt2u1qqqq")).toBe(200);
    expect(parseBolt11AmountSats("lightning:lnbc1u1qqqq")).toBe(100);
  });

  it("rejects junk", () => {
    expect(() => parseBolt11AmountSats("")).toThrow(InvoiceError);
    expect(() => parseBolt11AmountSats("chromescar71@phoenixwallet.me")).toThrow(InvoiceError);
    expect(() => parseBolt11AmountSats("bc1qnotaninvoice")).toThrow(InvoiceError);
    expect(() => parseBolt11AmountSats("lnbc")).toThrow(InvoiceError);
  });
});

describe("isBolt11", () => {
  it("spots invoices and leaves addresses alone", () => {
    expect(isBolt11("lnbc100n1qqqqqqqqqqqq")).toBe(true);
    expect(isBolt11("lightning:lnbc100n1qqqqqqqqqqqq")).toBe(true);
    expect(isBolt11("me@wallet.example")).toBe(false);
    expect(isBolt11("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")).toBe(false);
  });
});

// BOLT11 spec vector: 250,000 sat donation invoice. payment_hash is 000102…0102.
const SPEC_2500U =
  "lnbc2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpuaztrnwngzn3kdzw5hydlzf03qdgm2hdq27cqv3agm2awhz5se903vruatfhq77w3ls4evs3ch9zw97j25emudupq63nyw24cg27h2rspfj9srp";
const SPEC_HASH = "0001020304050607080900010203040506070809000102030405060708090102";

describe("parseBolt11PaymentHash and preimage check", () => {
  it("reads the payment hash from a BOLT11 spec invoice, including a lightning: prefix", () => {
    expect(parseBolt11PaymentHash(SPEC_2500U)).toBe(SPEC_HASH);
    expect(parseBolt11PaymentHash("lightning:" + SPEC_2500U.toUpperCase())).toBe(SPEC_HASH);
    expect(parseBolt11AmountSats(SPEC_2500U)).toBe(250_000);
  });

  it("accepts a preimage that hashes to the invoice payment hash, and refuses a wrong one", async () => {
    const preimage = "aa".repeat(32);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", hexToBytes(preimage)));
    const invoice = makeInvoiceWithPaymentHash(digest);
    expect(parseBolt11PaymentHash(invoice)).toBe(hex(digest));
    expect(await preimageMatchesInvoice(invoice, preimage)).toBe(true);
    expect(await preimageMatchesInvoice(invoice, "0x" + preimage.toUpperCase())).toBe(true);
    expect(await preimageMatchesInvoice(invoice, "bb".repeat(32))).toBe(false);
    await expect(assertPreimageMatchesInvoice(invoice, "bb".repeat(32))).rejects.toThrow(InvoiceError);
  });

  it("rejects a short or odd preimage", async () => {
    const invoice = makeInvoiceWithPaymentHash(new Uint8Array(32));
    await expect(preimageMatchesInvoice(invoice, "aa")).rejects.toThrow(/32 bytes/);
  });
});

function hexToBytes(h: string): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(h.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function hex(bytes: Uint8Array): string {
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** Minimal lnbc…1 invoice carrying only a payment-hash tagged field. Checksum is fake. */
function makeInvoiceWithPaymentHash(hash: Uint8Array): string {
  const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";
  const bits: number[] = Array(7).fill(0); // timestamp
  bits.push(1, 1, 20); // type 1, length 52 (260 bits)
  let acc = 0;
  let n = 0;
  for (const b of hash) {
    acc = (acc << 8) | b;
    n += 8;
    while (n >= 5) {
      n -= 5;
      bits.push((acc >> n) & 31);
    }
  }
  if (n > 0) bits.push((acc << (5 - n)) & 31);
  bits.push(...Array(104).fill(0)); // signature
  return "lnbc1" + bits.map((w) => CHARSET[w]).join("") + "qqqqqq";
}
