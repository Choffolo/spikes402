import { describe, expect, it } from "vitest";
import { encodeBolt11ForTest } from "./invoice";
import { looksLikeLightningAddress, parseLightningAddress, resolveLightningAddress, UNSUPPORTED_ADDRESS } from "./lnaddress";

describe("parseLightningAddress", () => {
  it("reads plain addresses, a leading ₿, and a lightning: prefix", () => {
    expect(parseLightningAddress("chromescar71@phoenixwallet.me")).toEqual({ user: "chromescar71", domain: "phoenixwallet.me" });
    expect(parseLightningAddress("₿chromescar71@PhoenixWallet.me")).toEqual({ user: "chromescar71", domain: "phoenixwallet.me" });
    expect(parseLightningAddress(" ₿ bob@example.com ")).toEqual({ user: "bob", domain: "example.com" });
    expect(parseLightningAddress("lightning:bob@example.com")).toEqual({ user: "bob", domain: "example.com" });
  });

  it("refuses ports, IPs, local names, paths and junk", () => {
    for (const bad of [
      "bob@example.com:8080", "bob@127.0.0.1", "bob@10.0.0.1", "bob@localhost", "bob@nas.local", "bob@intranet",
      "bob@example.com/x", "bob@@example.com", "@example.com", "bob", "bob@exa mple.com", "bob@-bad.com",
    ]) {
      expect(parseLightningAddress(bad), bad).toBeNull();
    }
    expect(looksLikeLightningAddress("bob@example.com")).toBe(true);
    expect(looksLikeLightningAddress("lnbc1...")).toBe(false);
  });
});

const addr = { user: "bob", domain: "example.com" };
const inv = (sats: number) => encodeBolt11ForTest(`lnbc${sats * 10}n`); // 10n = 1 sat

function server(opts: { params?: object | number; reply?: object | number; calls?: string[] }) {
  return (async (url: string | URL | Request) => {
    const u = String(url);
    opts.calls?.push(u);
    const body = u.includes("/.well-known/lnurlp/") ? opts.params : opts.reply;
    if (typeof body === "number") return new Response("nope", { status: body });
    return new Response(JSON.stringify(body ?? {}), { status: 200 });
  }) as typeof fetch;
}
const goodParams = { tag: "payRequest", callback: "https://example.com/cb?k=1", minSendable: 1000, maxSendable: 100_000_000 };

describe("resolveLightningAddress", () => {
  it("asks the callback for the amount in msats and returns an invoice of exactly that amount", async () => {
    const calls: string[] = [];
    const r = await resolveLightningAddress(addr, 21, server({ params: goodParams, reply: { pr: inv(21) }, calls }));
    expect(r.invoice).toBe(inv(21));
    expect(calls[0]).toBe("https://example.com/.well-known/lnurlp/bob");
    expect(calls[1]).toBe("https://example.com/cb?k=1&amount=21000");
  });

  it("refuses an invoice for a different amount, or without one", async () => {
    await expect(resolveLightningAddress(addr, 21, server({ params: goodParams, reply: { pr: inv(2100) } }))).rejects.toThrow(/different amount/);
    await expect(resolveLightningAddress(addr, 21, server({ params: goodParams, reply: { pr: encodeBolt11ForTest("lnbc") } }))).rejects.toThrow(/different amount/);
    await expect(resolveLightningAddress(addr, 21, server({ params: goodParams, reply: { pr: "junk" } }))).rejects.toThrow(/different amount/);
  });

  it("refuses a callback that is not https or not on the same host", async () => {
    for (const callback of ["http://example.com/cb", "https://evil.example/cb", "https://example.com.evil.example/cb", "https://example.com:444/cb", "https://u:p@example.com/cb", "not a url"]) {
      await expect(resolveLightningAddress(addr, 21, server({ params: { ...goodParams, callback }, reply: { pr: inv(21) } })), callback).rejects.toThrow();
    }
  });

  it("checks the min and max the address accepts and says what they are", async () => {
    const p = { ...goodParams, minSendable: 10_000, maxSendable: 50_000 };
    await expect(resolveLightningAddress(addr, 5, server({ params: p }))).rejects.toThrow(/between 10 and 50 sats/);
    await expect(resolveLightningAddress(addr, 51, server({ params: p }))).rejects.toThrow(/between 10 and 50 sats/);
    await expect(resolveLightningAddress(addr, 10, server({ params: p, reply: { pr: inv(10) } }))).resolves.toBeDefined();
  });

  it("gives the plain 'not supported' message for BOLT12-only or non-LNURL addresses", async () => {
    await expect(resolveLightningAddress(addr, 21, server({ params: 404 }))).rejects.toThrow(UNSUPPORTED_ADDRESS);
    await expect(resolveLightningAddress(addr, 21, (async () => { throw new TypeError("Failed to fetch"); }) as typeof fetch)).rejects.toThrow(UNSUPPORTED_ADDRESS);
    await expect(resolveLightningAddress(addr, 21, server({ params: { tag: "withdrawRequest" } }))).rejects.toThrow(UNSUPPORTED_ADDRESS);
  });

  it("shows the provider's reason when it refuses, and needs an amount", async () => {
    await expect(resolveLightningAddress(addr, 21, server({ params: goodParams, reply: { status: "ERROR", reason: "Out of liquidity" } }))).rejects.toThrow(/Out of liquidity/);
    await expect(resolveLightningAddress(addr, 0, server({ params: goodParams }))).rejects.toThrow(/how many sats/);
  });

  it("does not follow redirects or send credentials", async () => {
    let init: RequestInit | undefined;
    const f = (async (_u: string, i?: RequestInit) => {
      init = i;
      return new Response(JSON.stringify(goodParams));
    }) as unknown as typeof fetch;
    await resolveLightningAddress(addr, 21, f).catch(() => undefined);
    expect(init).toMatchObject({ redirect: "error", credentials: "omit" });
  });
});
