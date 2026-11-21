import { describe, expect, it } from "vitest";
import { historyRow, siteHost } from "./format";

describe("siteHost", () => {
  it("shows the host only, not the URL", () => {
    expect(siteHost("https://www.example.com")).toBe("www.example.com");
    expect(siteHost("http://127.0.0.1:8402")).toBe("127.0.0.1:8402");
    expect(siteHost("popup")).toBe("This wallet");
    expect(siteHost("x".repeat(200)).length).toBeLessThanOrEqual(40);
  });
});

describe("historyRow", () => {
  const base = { time: "2026-11-21T17:00:00Z", origin: "https://a.example", destination: "lnbc1…", detail: "preimage deadbeef".repeat(10) };

  it("gives price, fee and total for a paid page", () => {
    const row = historyRow({ ...base, result: "sent", amountSats: 6, priceSats: 2, feeSats: 4 });
    expect(row).toMatchObject({ site: "a.example", status: "paid", price: 2, fee: 4, total: 6 });
  });

  it("works for older records that have no price or fee", () => {
    expect(historyRow({ ...base, result: "sent", amountSats: 7 })).toMatchObject({ price: 7, total: 7 });
  });

  it("does not claim a fee for a payment that did not happen, and never carries the detail text", () => {
    const failed = historyRow({ ...base, result: "failed", amountSats: 5, priceSats: 5 });
    expect(failed).toMatchObject({ status: "failed", price: 5, total: 5 });
    expect(failed.fee).toBeUndefined();
    expect(JSON.stringify(failed)).not.toMatch(/preimage|deadbeef|lnbc/);
    expect(historyRow({ ...base, result: "refused", amountSats: 5 }).status).toBe("declined");
    expect(historyRow({ ...base, result: "pending", amountSats: 10, priceSats: 5 }).status).toBe("waiting");
  });
});
