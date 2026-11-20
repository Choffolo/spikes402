import { describe, expect, it } from "vitest";
import { decidePayment, defaultBudget, emptySpend, recordSpend, spendForToday } from "./budget";

const origin = "https://example.com";
const today = "2026-11-20";
const base = { origin, settings: defaultBudget, spend: emptySpend(today), today };

describe("decidePayment", () => {
  it("refuses amountless invoices and bad amounts", () => {
    expect(decidePayment({ ...base, amountSats: null }).action).toBe("refuse");
    expect(decidePayment({ ...base, amountSats: 0 }).action).toBe("refuse");
    expect(decidePayment({ ...base, amountSats: 1.5 }).action).toBe("refuse");
  });

  it("refuses amounts above the per-payment cap", () => {
    const d = decidePayment({ ...base, amountSats: 11 });
    expect(d.action).toBe("refuse");
    if (d.action !== "refuse") throw new Error("expected refuse");
    expect(d.reason).toMatch(/per-payment cap of 10/);
  });

  it("pays silently when auto-pay is on and all caps fit", () => {
    expect(decidePayment({ ...base, amountSats: 10 })).toEqual({ action: "pay" });
  });

  it("asks for confirmation when auto-pay is off, even inside the budget", () => {
    const d = decidePayment({
      ...base,
      amountSats: 2,
      settings: { ...defaultBudget, autoPay: false },
    });
    expect(d.action).toBe("confirm");
  });

  it("asks for confirmation when the site daily cap would be exceeded", () => {
    const spend = recordSpend(emptySpend(today), today, origin, 95);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.reason).toMatch(/example.com/);
  });

  it("asks for confirmation when the global daily cap would be exceeded", () => {
    const spend = recordSpend(emptySpend(today), today, "https://other.test", 495);
    const d = decidePayment({ ...base, amountSats: 10, spend });
    expect(d.action).toBe("confirm");
    if (d.action !== "confirm") throw new Error("expected confirm");
    expect(d.reason).toMatch(/total spend/);
  });

  it("resets spend on a new day", () => {
    const yesterday = recordSpend(emptySpend("2026-11-19"), "2026-11-19", origin, 100);
    expect(spendForToday(yesterday, today)).toEqual(emptySpend(today));
    expect(decidePayment({ ...base, amountSats: 10, spend: yesterday }).action).toBe("pay");
  });
});
