import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { encodeBolt11ForTest } from "./invoice";
import { maxFeeSatsFor } from "./spark-util";
import type { PendingPayment, Reservation } from "./types";

type Listener = (msg: unknown, sender: unknown, send: (r: unknown) => void) => boolean | void;
type Reply = Record<string, unknown>;

let store: Record<string, unknown>;
let listener: Listener;
let walletSend: (msg: Record<string, unknown>) => Promise<Reply>;
let walletLookup: (msg: Record<string, unknown>) => Promise<Reply>;
let sends: Record<string, unknown>[];

const OK = (feeSats?: number): Reply => ({ preimage: "00".repeat(32), detail: "ok", ...(feeSats === undefined ? {} : { feeSats }) });
const PENDING_REPLY: Reply = { error: "Timed out waiting for the Lightning payment preimage.", uncertain: true, requestId: "req-1" };

function installChrome() {
  store = {};
  sends = [];
  walletSend = async () => OK();
  walletLookup = async () => ({ state: "pending" });
  const noop = async () => undefined;
  (globalThis as unknown as { chrome: unknown }).chrome = {
    runtime: {
      onInstalled: { addListener: () => undefined },
      onStartup: { addListener: () => undefined },
      onMessage: { addListener: (l: Listener) => (listener = l) },
      getURL: () => "chrome-extension://abc/",
      lastError: undefined,
      sendMessage: (msg: Record<string, unknown>, cb: (r: unknown) => void) => {
        if (msg.target !== "wallet") return;
        const done = (r: unknown) => queueMicrotask(() => cb(r));
        if (msg.type === "status") return done({ unlocked: true });
        if (msg.type === "send") {
          sends.push(msg);
          void walletSend(msg).then(done);
          return;
        }
        if (msg.type === "lookupSend") {
          void walletLookup(msg).then(done);
          return;
        }
        done({});
      },
    },
    tabs: { onRemoved: { addListener: () => undefined } },
    storage: {
      local: {
        get: async (k: string) => ({ [k]: structuredClone(store[k]) }),
        set: async (o: Record<string, unknown>) => {
          Object.assign(store, structuredClone(o));
        },
      },
    },
    action: { setBadgeText: noop, setTitle: noop, setBadgeBackgroundColor: noop, openPopup: noop },
    notifications: { create: noop },
  };
}

const pageSender = (origin: string, tab = 1) => ({ origin, url: origin + "/", tab: { id: tab, url: origin + "/" } });
const ext = { url: "chrome-extension://abc/index.html" };
const call = (msg: unknown, sender: unknown) =>
  new Promise<{ error?: string } & Record<string, unknown>>((res) => listener(msg, sender, res as (r: unknown) => void));
const pay = (origin: string, invoice: string, tab = 1) =>
  call({ type: "webln", method: "sendPayment", origin, invoice }, pageSender(origin, tab));
const answer = (id: string, choice: "once" | "always" | "refuse") => call({ type: "confirmPayment", id, choice }, ext);

type Stored = {
  spend?: { globalSats: number; perSite: Record<string, number> };
  history?: { result: string; detail: string; amountSats: number }[];
  prompts?: PendingPayment[];
  reservations?: Reservation[];
  settings?: { siteAllowances: Record<string, number>; maxSatsPerSitePerDay: number };
};
const state = () => (store["spikes402"] ?? {}) as Stored;
const tick = (ms = 20) => new Promise((r) => setTimeout(r, ms));

const small = () => encodeBolt11ForTest("lnbc50n"); // 5 sats, fee cap 5: costs 10, fits the default caps
const big = () => encodeBolt11ForTest("lnbc2500n"); // 250 sats: above the 10 sat cap, asks the user

async function boot() {
  vi.resetModules();
  await import("./background");
  await tick(1);
}

beforeEach(async () => {
  installChrome();
  await boot();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("a payment that does not finish in time", () => {
  it("stays counted in the budget as unconfirmed, not failed", async () => {
    walletSend = async () => PENDING_REPLY;
    const r = await pay("https://evil.example", small());
    expect(r.error).toMatch(/not been confirmed/);
    const s = state();
    expect(s.spend?.globalSats).toBe(10); // amount + fee cap are still reserved
    expect(s.reservations).toHaveLength(1);
    expect(s.reservations?.[0]).toMatchObject({ state: "uncertain", requestId: "req-1" });
    expect(s.history?.[0].result).toBe("pending");

    walletSend = async () => OK();
    expect((await pay("https://evil.example", small())).error).toBeUndefined();
    expect(state().spend?.globalSats).toBe(20); // the first payment was not forgotten
  });

  it("is released only when the wallet confirms the payment failed", async () => {
    walletSend = async () => PENDING_REPLY;
    await pay("https://evil.example", small());

    await call({ type: "getState" }, ext); // still pending: nothing changes
    await tick();
    expect(state().spend?.globalSats).toBe(10);
    expect(state().reservations).toHaveLength(1);

    walletLookup = async () => ({ state: "failed" });
    await call({ type: "getState" }, ext);
    await vi.waitFor(() => expect(state().reservations).toHaveLength(0));
    expect(state().spend?.globalSats).toBe(0);
    expect(state().history?.at(-1)?.result).toBe("failed");
  });

  it("is booked at the real cost when the wallet confirms it settled", async () => {
    walletSend = async () => PENDING_REPLY;
    await pay("https://evil.example", small());
    walletLookup = async () => ({ state: "settled", feeSats: 1 });
    await call({ type: "getState" }, ext);
    await vi.waitFor(() => expect(state().reservations).toHaveLength(0));
    expect(state().spend?.globalSats).toBe(6); // 5 sats + 1 sat real fee
    expect(state().history?.at(-1)).toMatchObject({ result: "sent", amountSats: 6 });
  });

  it("a clear wallet failure releases the reservation at once", async () => {
    walletSend = async () => ({ error: "Insufficient balance" });
    const r = await pay("https://evil.example", small());
    expect(r.error).toMatch(/Insufficient/);
    expect(state().spend?.globalSats).toBe(0);
    expect(state().reservations).toHaveLength(0);
    expect(state().history?.[0].result).toBe("failed");
  });

  it("a restart marks payments that were being sent as unconfirmed, and drops waiting prompts", async () => {
    store["spikes402"] = {
      spend: { day: "2026-11-21", globalSats: 10, perSite: {} },
      prompts: [{ id: "p", origin: "https://a.example", invoice: "x", amountSats: 1, costSats: 6, reason: "r", createdAt: 1 }],
      reservations: [
        { id: "r", origin: "https://a.example", day: "2026-11-21", amountSats: 5, feeSats: 5, invoice: "x", state: "in_flight", time: "t" },
      ],
    };
    await boot();
    expect(state().prompts).toEqual([]);
    expect(state().reservations?.[0].state).toBe("uncertain");
  });
});

describe("locking", () => {
  it("a hold invoice does not stall other payments, and caps stay race-free", async () => {
    let release!: () => void;
    walletSend = () => new Promise((res) => (release = () => res(OK())));
    const first = pay("https://hold.example", small(), 1);
    await tick();
    expect(sends).toHaveLength(1);

    walletSend = async () => OK();
    const second = await pay("https://other.example", small(), 2); // returns while the first is still pending
    expect(second.error).toBeUndefined();
    expect(state().reservations).toHaveLength(1);
    release();
    expect((await first).error).toBeUndefined();
    expect(state().spend?.globalSats).toBe(20);
    expect(state().reservations).toHaveLength(0);
  });

  it("two payments cannot both pass a cap that fits only one", async () => {
    store["spikes402"] = { settings: { maxSatsPerPayment: 10, maxSatsPerSitePerDay: 15, maxSatsPerDay: 500, siteAllowances: {}, network: "mainnet" } };
    await boot();
    walletSend = () => new Promise(() => undefined); // both would be in flight together
    void pay("https://a.example", small(), 1);
    void pay("https://a.example", small(), 2);
    await tick();
    expect(sends).toHaveLength(1); // the second one is asked about, not paid
    expect(state().prompts).toHaveLength(1);
    expect(state().spend?.globalSats).toBe(10);
  });
});

describe("confirmation prompts", () => {
  it("queues prompts instead of overwriting one slot", async () => {
    const p1 = pay("https://one.example", big(), 1);
    const p2 = pay("https://two.example", big(), 2);
    await tick();
    const prompts = state().prompts ?? [];
    expect(prompts.map((p) => p.origin).sort()).toEqual(["https://one.example", "https://two.example"]);

    const snap = (await call({ type: "getState" }, ext)) as unknown as { pending?: PendingPayment; pendingCount: number };
    expect(snap.pendingCount).toBe(2);

    const first = prompts.find((p) => p.origin === "https://one.example")!;
    expect((await answer(first.id, "once")).error).toBeUndefined();
    expect((await p1).error).toBeUndefined();
    expect(state().prompts?.map((p) => p.origin)).toEqual(["https://two.example"]); // the other is still waiting

    expect((await answer(prompts.find((p) => p.origin === "https://two.example")!.id, "refuse")).error).toBeUndefined();
    expect((await p2).error).toMatch(/declined/);
  });

  it("an expiry timer only removes its own prompt", async () => {
    vi.useFakeTimers();
    const p1 = pay("https://one.example", big(), 1);
    await vi.advanceTimersByTimeAsync(10);
    await vi.advanceTimersByTimeAsync(2 * 60 * 1000);
    const p2 = pay("https://two.example", big(), 2);
    await vi.advanceTimersByTimeAsync(10);
    expect(state().prompts).toHaveLength(2);

    await vi.advanceTimersByTimeAsync(3 * 60 * 1000 + 100); // the 1st prompt (5 min old) expires, the 2nd is 3 min old
    expect((await p1).error).toMatch(/declined/);
    expect(state().prompts?.map((p) => p.origin)).toEqual(["https://two.example"]);

    const second = state().prompts![0];
    expect((await answer(second.id, "once")).error).toBeUndefined();
    expect((await p2).error).toBeUndefined();
  });

  it("an expired or answered prompt cannot be answered again or store an 'always' allowance", async () => {
    vi.useFakeTimers();
    const p = pay("https://one.example", big(), 1);
    await vi.advanceTimersByTimeAsync(10);
    const id = state().prompts![0].id;
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000 + 100);
    await p.catch(() => undefined);
    expect(state().prompts).toEqual([]);

    expect((await answer(id, "always")).error).toMatch(/expired/);
    expect(state().settings?.siteAllowances ?? {}).toEqual({});
    expect(sends).toHaveLength(0);

    vi.useRealTimers();
    const q = pay("https://two.example", big(), 2);
    await tick();
    const id2 = state().prompts![0].id;
    expect((await answer(id2, "refuse")).error).toBeUndefined();
    await q.catch(() => undefined);
    expect((await answer(id2, "always")).error).toMatch(/expired/); // a declined prompt cannot be revived
    expect(state().settings?.siteAllowances ?? {}).toEqual({});
  });
});

describe("fee cap", () => {
  it("gives the wallet the same fee cap the budget counted", async () => {
    const q = pay("https://one.example", big(), 1);
    await tick();
    await answer(state().prompts![0].id, "once");
    await q;
    expect(sends[0].maxFeeSats).toBe(maxFeeSatsFor(250)); // 25, not the SDK default of 5
    expect(state().spend?.globalSats).toBe(250 + 25); // fee not reported: the cap is what was counted

    await pay("https://two.example", small(), 2);
    expect(sends[1].maxFeeSats).toBe(maxFeeSatsFor(5));
  });
});
