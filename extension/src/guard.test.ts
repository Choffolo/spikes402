import { describe, expect, it } from "vitest";
import {
  CLOCK_SLACK_MS,
  SAME_PAGE_MS,
  SITE_RATE_MS,
  checkAuto,
  checkInvoice,
  clockWentBack,
  elapsedSince,
  emptyGuard,
  pageId,
  recordFailed,
  recordPayment,
  recordSettled,
  type Stamp,
} from "./guard";
import { buildInvoiceForTest, parseBolt11Info } from "./invoice";

const T = 1_800_000_000_000; // some moment, in ms
const clock = (wall: number, mono: number, boot = "run-1"): Stamp => ({ wall, mono, boot });
const hash = (n: number) => String(n).padStart(64, "0");
const pay = (g = emptyGuard(), c = clock(T, 1_000), page = "a.example/x", site = "a.example", h = hash(1)) =>
  recordPayment(g, c, { site, page, hash: h });

describe("same page, and the rate limit", () => {
  it("does not pay the same page again for 60 seconds, then does", () => {
    const g = pay();
    expect(checkAuto(g, clock(T + 59_000, 60_000), "a.example", "a.example/x")?.cause).toBe("same_page");
    expect(checkAuto(g, clock(T + SAME_PAGE_MS, 61_000), "a.example", "a.example/x")).toBeUndefined();
  });

  it("allows one automatic payment per site every few seconds, for any page", () => {
    const g = pay();
    expect(checkAuto(g, clock(T + SITE_RATE_MS - 1, 1_000 + SITE_RATE_MS - 1), "a.example", "a.example/other")?.cause).toBe("rate_limit");
    expect(checkAuto(g, clock(T + SITE_RATE_MS, 1_000 + SITE_RATE_MS), "a.example", "a.example/other")).toBeUndefined();
    expect(checkAuto(g, clock(T + 1, 1_001), "b.example", "b.example/x")).toBeUndefined(); // another site is not slowed
  });

  it("the page's quiet time restarts when the payment settles", () => {
    const g = recordSettled(pay(), clock(T + 30_000, 31_000), "a.example/x");
    expect(checkAuto(g, clock(T + 80_000, 81_000), "a.example", "a.example/x")?.cause).toBe("same_page"); // 50 s after settling
    expect(checkAuto(g, clock(T + 91_000, 92_000), "a.example", "a.example/x")).toBeUndefined();
  });

  it("a failed payment frees the page and the invoice, but not the site's rate limit", () => {
    const g = recordFailed(pay(), "a.example/x", hash(1));
    expect(g.pages).toEqual({});
    expect(g.hashes).toEqual({});
    expect(checkAuto(g, clock(T + 1_000, 2_000), "a.example", "a.example/x")?.cause).toBe("rate_limit");
  });

  it("the page is the site plus the path: no query, no subdomain", () => {
    expect(pageId("example.com", "https://shop.example.com/a/b?x=1#h")).toBe("example.com/a/b");
    expect(pageId("example.com", undefined)).toBe("example.com/");
    expect(pageId("example.com", "not a url")).toBe("example.com/");
  });
});

describe("invoices", () => {
  const make = (o: { prefix?: string; timestampSec?: number; expirySec?: number; n?: number } = {}) =>
    parseBolt11Info(buildInvoiceForTest({ amount: "20n", hash: new Uint8Array(32).fill(o.n ?? 1), ...o }));
  const now = clock(T, 1_000);

  it("an invoice already paid or in flight is refused; a failed one can be tried again", () => {
    const info = make({ timestampSec: T / 1000 });
    expect(checkInvoice(info, "mainnet", emptyGuard(), now)).toBeUndefined();
    const g = pay(emptyGuard(), now, "a.example/x", "a.example", info.paymentHash);
    expect(checkInvoice(info, "mainnet", g, now)).toMatch(/already paid/);
    expect(checkInvoice(info, "mainnet", recordFailed(g, undefined, info.paymentHash), now)).toBeUndefined();
  });

  it("refuses the wrong network and expired invoices", () => {
    expect(checkInvoice(make({ prefix: "bcrt", timestampSec: T / 1000 }), "mainnet", emptyGuard(), now)).toMatch(/another Bitcoin network/);
    expect(checkInvoice(make({ prefix: "bc", timestampSec: T / 1000 }), "regtest", emptyGuard(), now)).toMatch(/another Bitcoin network/);
    expect(checkInvoice(make({ timestampSec: T / 1000 - 7_200 }), "mainnet", emptyGuard(), now)).toMatch(/expired/);
    expect(checkInvoice(make({ timestampSec: T / 1000 - 7_200, expirySec: 10_000 }), "mainnet", emptyGuard(), now)).toBeUndefined();
  });
});

describe("a clock moved backwards", () => {
  it("stops automatic payments and asks, even in the same service-worker run", () => {
    const g = pay();
    const back = clock(T - 3_600_000, 100_000); // an hour back, 99 s later by the monotonic clock
    expect(clockWentBack(g, back)).toBe(true);
    expect(checkAuto(g, back, "other.example", "other.example/y")?.cause).toBe("clock_rollback");
  });

  it("does the same after the service worker restarted (new run, only the saved wall time is left)", () => {
    const g = pay();
    const back = clock(T - 3_600_000, 50, "run-2");
    expect(checkAuto(g, back, "other.example", "other.example/y")?.cause).toBe("clock_rollback");
  });

  it("never shortens a cooldown: time stands still until the clock passes the highest time seen", () => {
    const g = pay();
    const stamp = g.pages["a.example/x"];
    expect(elapsedSince(stamp, g, clock(T - 120_000, 130_000, "run-2"))).toBe(0);
    expect(elapsedSince(stamp, g, clock(T + 10_000, 20_000, "run-2"))).toBe(10_000);
  });

  it("a small correction (network time sync) is not a rollback, and still cannot shorten a cooldown", () => {
    const g = pay();
    const c = clock(T - CLOCK_SLACK_MS + 1, 2_000);
    expect(clockWentBack(g, c)).toBe(false);
    expect(checkAuto(g, c, "a.example", "a.example/x")?.cause).toBe("same_page");
  });

  it("the highest time seen never goes down", () => {
    const g = pay(pay(), clock(T - 3_600_000, 5_000), "b.example/x", "b.example", hash(2));
    expect(g.hw).toBe(T);
  });
});

describe("a clock moved forwards", () => {
  it("cannot end a cooldown early while the service worker keeps running (the monotonic clock says no)", () => {
    const g = pay();
    const jumped = clock(T + 3 * 3_600_000, 11_000); // 3 hours on the wall clock, 10 s on the monotonic one
    expect(checkAuto(g, jumped, "a.example", "a.example/x")?.cause).toBe("same_page");
    expect(elapsedSince(g.pages["a.example/x"], g, jumped)).toBe(10_000);
  });

  it("can end a cooldown after a service-worker restart: the residual risk", () => {
    const g = pay();
    const jumped = clock(T + 3 * 3_600_000, 50, "run-2");
    expect(checkAuto(g, jumped, "a.example", "a.example/x")).toBeUndefined();
  });

  it("a forward jump that is then undone looks like a rollback: payments ask until real time catches up", () => {
    const g = pay(pay(), clock(T + 3 * 3_600_000, 2_000)); // a payment made while the clock was ahead
    expect(checkAuto(g, clock(T + 1_000, 3_000, "run-2"), "c.example", "c.example/z")?.cause).toBe("clock_rollback");
  });
});

describe("what is kept", () => {
  it("drops old pages and sites, and keeps at most 200 invoice hashes (newest)", () => {
    let g = pay();
    g = pay(g, clock(T + 11 * 60_000, 700_000), "b.example/x", "b.example", hash(2)); // 11 minutes later
    expect(Object.keys(g.pages)).toEqual(["b.example/x"]);
    expect(Object.keys(g.sites)).toEqual(["b.example"]);
    for (let i = 0; i < 250; i++) g = pay(g, clock(T + 12 * 60_000 + i, 800_000 + i), "p/" + i, "s", hash(1000 + i));
    expect(Object.keys(g.hashes)).toHaveLength(200);
    expect(g.hashes[hash(1249)]).toBeDefined();
    expect(g.hashes[hash(1000)]).toBeUndefined();
  });
});
