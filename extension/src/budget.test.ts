import { describe, expect, it } from "vitest";
import {
  adjustSpend,
  type BudgetSettings,
  decidePayment,
  defaultBudget,
  effectivePaymentCap,
  emptySpend,
  recordSpend,
  spendForToday,
  withSiteAllowance,
  withSiteRule,
  withoutSite,
} from "./budget";


const site = "example.com";
const rule = { trusted: true, dailySats: 100, since: "2026-11-01T00:00:00Z" };
const trusted: BudgetSettings = { ...defaultBudget, sites: { [site]: rule, "other.test": rule } };
const today = "2026-11-20";
const base = { site, settings: trusted, spend: emptySpend(today), today };

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
    const spend = recordSpend(emptySpend(today), today, site, 95);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_site_day");
    expect(d.reason).toMatch(/example.com/);
  });

  it("asks when the global daily cap would be exceeded", () => {
    const spend = recordSpend(emptySpend(today), today, "other.test", 495);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_global_day");
    expect(d.reason).toMatch(/total spending/);
  });

  it("counts fees against the daily caps as well", () => {
    const spend = recordSpend(emptySpend(today), today, site, 90);
    const settings = { ...trusted, maxSatsPerPayment: 20 };
    const d = decidePayment({ ...base, settings, amountSats: 10, feeSats: 5, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.cause).toBe("over_site_day");
    expect(decidePayment({ ...base, settings, amountSats: 10, feeSats: 0, spend }).action).toBe("pay");
  });

  it("pays automatically when a site allowance covers this price", () => {
    const settings = withSiteAllowance(trusted, site, 15);
    expect(effectivePaymentCap(settings, site)).toBe(15);
    expect(decidePayment({ ...base, settings, amountSats: 10, feeSats: 5 }).action).toBe("pay");
    // Other sites still use the default cap.
    const other = decidePayment({ ...base, settings, site: "other.test", amountSats: 10, feeSats: 5 });
    expect(other.action).toBe("confirm");
  });

  it("resets spend on a new day", () => {
    const yesterday = recordSpend(emptySpend("2026-11-19"), "2026-11-19", site, 100);
    expect(spendForToday(yesterday, today)).toEqual(emptySpend(today));
    expect(decidePayment({ ...base, amountSats: 10, spend: yesterday }).action).toBe("pay");
  });
});

describe("withSiteAllowance", () => {
  it("keeps the higher of the existing allowance and the new price", () => {
    const once = withSiteAllowance(trusted, site, 15);
    const twice = withSiteAllowance(once, site, 12);
    expect(twice.siteAllowances[site]).toBe(15);
    const up = withSiteAllowance(once, site, 20);
    expect(up.siteAllowances[site]).toBe(20);
  });
});

describe("adjustSpend", () => {
  it("releases or tops up today's counters", () => {
    const spent = recordSpend(emptySpend(today), today, site, 10);
    expect(adjustSpend(spent, today, site, -10)).toEqual({ day: today, globalSats: 0, perSite: { [site]: 0 } });
    const up = adjustSpend(spent, today, site, 3);
    expect(up.globalSats).toBe(13);
    expect(up.perSite[site]).toBe(13);
  });

  it("does nothing for another day and never goes below zero", () => {
    const spent = recordSpend(emptySpend(today), today, site, 10);
    expect(adjustSpend(spent, "2026-11-19", site, -10)).toBe(spent);
    expect(adjustSpend(spent, today, site, -50).globalSats).toBe(0);
  });
});

describe("trusted sites", () => {
  it("refuses a site that is not on the list or was turned down", () => {
    const d = decidePayment({ ...base, site: "unknown.example", amountSats: 1 });
    expect(d).toMatchObject({ action: "refuse", reason: expect.stringMatching(/not allowed/) });
    const down = { ...trusted, sites: { ...trusted.sites, "no.example": { ...rule, trusted: false } } };
    expect(decidePayment({ ...base, settings: down, site: "no.example", amountSats: 1 }).action).toBe("refuse");
  });

  it("uses the site's own daily limit, not the default", () => {
    const small = { ...trusted, sites: { [site]: { ...rule, dailySats: 12 } } };
    const spend = recordSpend(emptySpend(today), today, site, 5);
    expect(decidePayment({ ...base, settings: small, spend, amountSats: 7 }).action).toBe("pay");
    const d = decidePayment({ ...base, settings: small, spend, amountSats: 8 });
    expect(d).toMatchObject({ action: "confirm", cause: "over_site_day" });
  });

  it("keeps the list bounded, dropping the oldest turned-down site first", () => {
    let s: BudgetSettings = defaultBudget;
    for (let i = 0; i < 200; i++) {
      s = withSiteRule(s, `n${i}.example`, { trusted: i === 199, dailySats: 1, since: new Date(Date.UTC(2026, 10, 1, 0, 0, i)).toISOString() });
    }
    expect(Object.keys(s.sites)).toHaveLength(200);
    const full = withSiteRule(s, "new.example", rule);
    expect(Object.keys(full.sites)).toHaveLength(200);
    expect(full.sites["new.example"]).toEqual(rule);
    expect(full.sites["n0.example"]).toBeUndefined(); // the oldest turned-down site made room
    expect(full.sites["n199.example"]?.trusted).toBe(true); // a trusted site is never dropped
  });

  it("forgetting a site also forgets its allowance", () => {
    const s = withoutSite(withSiteAllowance(trusted, site, 15), site);
    expect(s.sites[site]).toBeUndefined();
    expect(s.siteAllowances[site]).toBeUndefined();
    expect(s.sites["other.test"]).toEqual(rule);
  });
});
