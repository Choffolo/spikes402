import { describe, expect, it } from "vitest";
import { badgeColor, badgeTitle, formatBadgeText } from "./badge";

describe("formatBadgeText", () => {
  it("shows sats up to 9999, then thousands", () => {
    expect(formatBadgeText(1)).toBe("1");
    expect(formatBadgeText(10)).toBe("10");
    expect(formatBadgeText(9999)).toBe("9999");
    expect(formatBadgeText(10_000)).toBe("10k");
    expect(formatBadgeText(19999)).toBe("19k");
    expect(formatBadgeText(1_000_000)).toBe("1M+");
    expect(formatBadgeText(0)).toBe("");
  });
});

describe("badge copy and color", () => {
  it("changes the title and colour for paid and confirm states", () => {
    const base = { amountSats: 10, paid: false, needsConfirm: false };
    expect(badgeTitle(base)).toBe("This page costs 10 sats to open.");
    expect(badgeColor(base)).toBe("#44403c");
    expect(badgeTitle({ ...base, paid: true })).toMatch(/^Paid 10 sats/);
    expect(badgeColor({ ...base, paid: true })).toBe("#0f766e");
    expect(badgeTitle({ ...base, needsConfirm: true })).toMatch(/Confirm in the popup/);
    expect(badgeColor({ ...base, needsConfirm: true })).toBe("#b45309");
  });
});
