import { describe, expect, it } from "vitest";
import { InvoiceError, isBolt11, parseBolt11AmountSats } from "./invoice";

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

  it("accepts testnet and regtest prefixes", () => {
    expect(parseBolt11AmountSats("lntb100n1qqqq")).toBe(10);
    expect(parseBolt11AmountSats("lnbcrt2u1qqqq")).toBe(200);
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
    expect(isBolt11("me@wallet.example")).toBe(false);
    expect(isBolt11("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")).toBe(false);
  });
});
