import { beforeEach, describe, expect, it } from "vitest";
import { appendHistory, loadState, saveSettings, saveSpend, updateState } from "./storage";

let store: Record<string, unknown>;

beforeEach(() => {
  store = {};
  let n = 0;
  // Uneven delays: without a queue the reads and writes interleave.
  const delay = () => new Promise((r) => setTimeout(r, n++ % 2 === 0 ? 5 : 1));
  (globalThis as unknown as { chrome: unknown }).chrome = {
    storage: {
      local: {
        get: async (k: string) => {
          await delay();
          return { [k]: structuredClone(store[k]) };
        },
        set: async (o: Record<string, unknown>) => {
          await delay();
          Object.assign(store, structuredClone(o));
        },
      },
    },
  };
});

const rec = (i: number) => ({
  time: "t",
  origin: "o",
  amountSats: i,
  destination: "d",
  result: "refused" as const,
  detail: String(i),
});

describe("storage write queue", () => {
  it("never loses an update when helpers run at the same time", async () => {
    await Promise.all([
      saveSpend({ day: "2026-11-21", globalSats: 15, perSite: { "https://a": 15 } }),
      appendHistory(rec(1)),
      saveSettings({ maxSatsPerDay: 77 }),
      appendHistory(rec(2)),
    ]);
    const s = await loadState();
    expect(s.spend.globalSats).toBe(15);
    expect(s.settings.maxSatsPerDay).toBe(77);
    expect(s.history.map((h) => h.detail)).toEqual(["1", "2"]);
  });

  it("runs read-modify-write steps one after the other", async () => {
    await Promise.all(
      Array.from({ length: 20 }, () =>
        updateState((s) => {
          s.spend = { ...s.spend, globalSats: s.spend.globalSats + 1 };
        }),
      ),
    );
    expect((await loadState()).spend.globalSats).toBe(20);
  });

  it("writes nothing when the step throws, and keeps the queue working", async () => {
    await expect(
      updateState((s) => {
        s.spend = { ...s.spend, globalSats: 99 };
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");
    await appendHistory(rec(3));
    const s = await loadState();
    expect(s.spend.globalSats).toBe(0);
    expect(s.history).toHaveLength(1);
  });
});
