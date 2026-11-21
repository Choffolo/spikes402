import { describe, expect, it } from "vitest";
import {
  adjustSpend,
  decidePayment,
  defaultBudget,
  effectivePaymentCap,
  emptySpend,
  recordSpend,
  spendForToday,
  withSiteAllowance,
} from "./budget";

const origin = "https://example.com";
const today = "2026-11-20";
const base = { origin, settings: defaultBudget, spend: emptySpend(today), today };

describe("decidePayment", () => {
  it("refuses amountless invoices and bad amounts", () => {
    expect(decidePayment({ ...base, amountSats: null }).action).toBe("refuse");
    expect(decidePayment({ ...base, amountSats: 0 }).action).toBe("refuse");
    expect(decidePayment({ ...base, amountSats: 1.5 }).action).toBe("refuse");
  });

  it("pays automatically when the page price fits every cap", () => {
    expect(decidePayment({ ...base, amountSats: 10 })).toEqual({ action: "pay" });
    expect(decidePayment({ ...base, amountSats: 5, feeSats: 5 })).toEqual({ action: "pay" });
  });

  it("asks (does not refuse) when the price is above the per-page cap", () => {
    const d = decidePayment({ ...base, amountSats: 11 });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_payment_cap");
    expect(d.reason).toMatch(/more than your limit for one page \(10 sats, fee included\)/);
  });

  it("counts fees toward the per-page cap", () => {
    // 10 sat invoice + 5 sat fee is 15, above the 10-sat cap.
    const d = decidePayment({ ...base, amountSats: 10, feeSats: 5 });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_payment_cap");
    expect(d.reason).toMatch(/limit for one page/);
    expect(decidePayment({ ...base, amountSats: 10, feeSats: 0 }).action).toBe("pay");
  });

  it("asks when the site daily cap would be exceeded", () => {
    const spend = recordSpend(emptySpend(today), today, origin, 95);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_site_day");
    expect(d.reason).toMatch(/example.com/);
  });

  it("asks when the global daily cap would be exceeded", () => {
    const spend = recordSpend(emptySpend(today), today, "https://other.test", 495);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_global_day");
    expect(d.reason).toMatch(/total spending/);
  });

  it("counts fees against the daily caps as well", () => {
    const spend = recordSpend(emptySpend(today), today, origin, 90);
    const settings = { ...defaultBudget, maxSatsPerPayment: 20 };
    const d = decidePayment({ ...base, settings, amountSats: 10, feeSats: 5, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_site_day");
    expect(decidePayment({ ...base, settings, amountSats: 10, feeSats: 0, spend }).action).toBe("pay");
  });

  it("pays automatically when a site allowance covers this price", () => {
    const settings = withSiteAllowance(defaultBudget, origin, 15);
    expect(effectivePaymentCap(settings, origin)).toBe(15);
    expect(decidePayment({ ...base, settings, amountSats: 10, feeSats: 5 }).action).toBe("pay");
    // Other sites still use the default cap.
    const other = decidePayment({ ...base, settings, origin: "https://other.test", amountSats: 10, feeSats: 5 });
    expect(other.action).toBe("confirm");
  });

  it("resets spend on a new day", () => {
    const yesterday = recordSpend(emptySpend("2026-11-19"), "2026-11-19", origin, 100);
    expect(spendForToday(yesterday, today)).toEqual(emptySpend(today));
    expect(decidePayment({ ...base, amountSats: 10, spend: yesterday }).action).toBe("pay");
  });
});

describe("withSiteAllowance", () => {
  it("keeps the higher of the existing allowance and the new price", () => {
    const once = withSiteAllowance(defaultBudget, origin, 15);
    const twice = withSiteAllowance(once, origin, 12);
    expect(twice.siteAllowances[origin]).toBe(15);
    const up = withSiteAllowance(once, origin, 20);
    expect(up.siteAllowances[origin]).toBe(20);
  });
});

describe("adjustSpend", () => {
  it("releases or tops up today's counters", () => {
    const spent = recordSpend(emptySpend(today), today, origin, 10);
    expect(adjustSpend(spent, today, origin, -10)).toEqual({ day: today, globalSats: 0, perSite: { [origin]: 0 } });
    const up = adjustSpend(spent, today, origin, 3);
    expect(up.globalSats).toBe(13);
    expect(up.perSite[origin]).toBe(13);
  });

  it("does nothing for another day and never goes below zero", () => {
    const spent = recordSpend(emptySpend(today), today, origin, 10);
    expect(adjustSpend(spent, "2026-11-19", origin, -10)).toBe(spent);
    expect(adjustSpend(spent, today, origin, -50).globalSats).toBe(0);
  });
});
