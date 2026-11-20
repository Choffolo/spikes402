import { describe, expect, it } from "vitest";
import {
  looksLikeSparkAddress,
  lightningHint,
  maxFeeSatsFor,
  sendRequestFailed,
  sendRequestHasPreimage,
  toSparkNetwork,
} from "./spark-util";

describe("toSparkNetwork", () => {
  it("maps our network names to the SDK identifiers", () => {
    expect(toSparkNetwork("mainnet")).toBe("MAINNET");
    expect(toSparkNetwork("regtest")).toBe("REGTEST");
  });
});

describe("lightningHint", () => {
  it("treats both networks as ready, with a regtest caution", () => {
    expect(lightningHint("mainnet").ready).toBe(true);
    expect(lightningHint("regtest").ready).toBe(true);
    expect(lightningHint("regtest").hint).toMatch(/no real money/i);
  });
});

describe("maxFeeSatsFor", () => {
  it("allows at least 5 sats and 10% of larger amounts", () => {
    expect(maxFeeSatsFor(10)).toBe(5);
    expect(maxFeeSatsFor(100)).toBe(10);
    expect(maxFeeSatsFor(-1)).toBe(5);
  });
});

describe("looksLikeSparkAddress", () => {
  it("accepts common Spark bech32 prefixes and rejects invoices and junk", () => {
    expect(looksLikeSparkAddress("sp1qabcdefghijklmnopqrstuv")).toBe(true);
    expect(looksLikeSparkAddress("spark1qabcdefghijklmnopqrstuv")).toBe(true);
    expect(looksLikeSparkAddress("sprt1qabcdefghijklmnopqrstuv")).toBe(true);
    expect(looksLikeSparkAddress("lnbc10n1...")).toBe(false);
    expect(looksLikeSparkAddress("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4")).toBe(false);
  });
});

describe("send request helpers", () => {
  it("detects failed statuses and a present preimage", () => {
    expect(sendRequestFailed("LIGHTNING_PAYMENT_FAILED")).toBe(true);
    expect(sendRequestFailed("LIGHTNING_PAYMENT_INITIATED")).toBe(false);
    expect(sendRequestHasPreimage({ paymentPreimage: "ab" })).toBe("ab");
    expect(sendRequestHasPreimage({ paymentPreimage: "  " })).toBeUndefined();
    expect(sendRequestHasPreimage(null)).toBeUndefined();
  });
});
