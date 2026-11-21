import { isBolt11, parseBolt11AmountSats } from "./invoice";

/**
 * Lightning addresses (name@domain), paid through LNURL-pay (LUD-16):
 * GET https://domain/.well-known/lnurlp/name, then the callback it names.
 * The wallet only follows https, never a redirect, and only a callback on
 * the same host. Before anything is paid, the invoice it returns must be
 * for exactly the amount the user typed. The fee cap is applied by the caller.
 *
 * Addresses that are only a BIP353 / BOLT12 offer (Phoenix, for example) have
 * no LNURL endpoint; the Spark SDK cannot pay BOLT12, so they get a clear error.
 */
export const UNSUPPORTED_ADDRESS = "This address type isn't supported yet. Paste an invoice instead.";

const MAX_BODY_CHARS = 64 * 1024;
const TIMEOUT_MS = 10_000;

export type LightningAddress = { user: string; domain: string };

/** Accepts "name@domain", with an optional leading ₿ or "lightning:". Returns null if it is not an address. */
export function parseLightningAddress(input: string): LightningAddress | null {
  let s = input.trim().replace(/^\u20bf\s*/, "").replace(/^lightning:/i, "").trim();
  s = s.toLowerCase();
  const m = s.match(/^([a-z0-9._+-]{1,64})@([a-z0-9.-]{3,253})$/);
  if (!m) return null;
  const [, user, domain] = m;
  const labels = domain.split(".");
  if (labels.length < 2) return null;
  if (!labels.every((l) => /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(l))) return null;
  if (/^\d+$/.test(labels[labels.length - 1])) return null; // an IP address, not a name
  if (/\.(local|localhost|internal|onion)$/.test(domain)) return null;
  return { user, domain };
}

export function looksLikeLightningAddress(input: string): boolean {
  return input.includes("@");
}

type Fetch = typeof fetch;

async function getJson(url: string, fetchFn: Fetch): Promise<Record<string, unknown>> {
  const res = await fetchFn(url, {
    redirect: "error",
    credentials: "omit",
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const text = await res.text();
  if (text.length > MAX_BODY_CHARS) throw new Error("answer too large");
  const body: unknown = JSON.parse(text);
  if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("not an object");
  return body as Record<string, unknown>;
}

const plain = (s: unknown): string => String(s ?? "").replace(/[^\x20-\x7e]/g, "").slice(0, 200);

/** The invoice to pay for `amountSats` to this address. Throws an error a person can read. */
export async function resolveLightningAddress(
  address: LightningAddress,
  amountSats: number,
  fetchFn: Fetch = fetch,
): Promise<{ invoice: string }> {
  if (!Number.isSafeInteger(amountSats) || amountSats < 1) {
    throw new Error("Enter how many sats to send to this address.");
  }
  const who = `${address.user}@${address.domain}`;

  let params: Record<string, unknown>;
  try {
    params = await getJson(`https://${address.domain}/.well-known/lnurlp/${encodeURIComponent(address.user)}`, fetchFn);
  } catch {
    // No such endpoint, no answer, or not LNURL (a BOLT12-only address like Phoenix's).
    throw new Error(UNSUPPORTED_ADDRESS);
  }
  if (params.status === "ERROR") throw new Error(`${who}: ${plain(params.reason) || "the provider refused the request."}`);
  const { callback, minSendable, maxSendable } = params;
  if (params.tag !== "payRequest" || typeof callback !== "string") throw new Error(UNSUPPORTED_ADDRESS);

  let cb: URL;
  try {
    cb = new URL(callback);
  } catch {
    throw new Error(UNSUPPORTED_ADDRESS);
  }
  if (cb.protocol !== "https:" || cb.hostname !== address.domain || cb.port !== "" || cb.username || cb.password) {
    throw new Error(`${who} sent a payment link that points somewhere else, so it was not used.`);
  }

  if (
    typeof minSendable !== "number" || typeof maxSendable !== "number" ||
    !Number.isSafeInteger(minSendable) || !Number.isSafeInteger(maxSendable) ||
    minSendable < 1 || maxSendable < minSendable
  ) {
    throw new Error(UNSUPPORTED_ADDRESS);
  }
  const msats = amountSats * 1000;
  if (msats < minSendable || msats > maxSendable) {
    const lo = Math.ceil(minSendable / 1000);
    const hi = Math.floor(maxSendable / 1000);
    throw new Error(`${who} accepts between ${lo} and ${hi} sats. Change the amount.`);
  }

  cb.searchParams.set("amount", String(msats));
  let reply: Record<string, unknown>;
  try {
    reply = await getJson(cb.toString(), fetchFn);
  } catch {
    throw new Error(`${who} did not give an invoice. Try again later, or paste an invoice.`);
  }
  if (reply.status === "ERROR") throw new Error(`${who}: ${plain(reply.reason) || "the provider refused the request."}`);

  const pr = typeof reply.pr === "string" ? reply.pr.trim() : "";
  let encoded: number | null = null;
  try {
    encoded = isBolt11(pr) ? parseBolt11AmountSats(pr) : null;
  } catch {
    encoded = null;
  }
  if (encoded !== amountSats) {
    throw new Error(`${who} gave an invoice for a different amount, so nothing was paid.`);
  }
  return { invoice: pr };
}
