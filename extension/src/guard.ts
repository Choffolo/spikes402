import type { ConfirmCause } from "./budget";
import { invoiceExpired, invoiceMatchesNetwork, type Bolt11Info } from "./invoice";
import type { Network } from "./types";

/**
 * Guards against a hostile page that keeps asking for small payments.
 *
 * - Same page: after a payment, the same page (site + path) is not paid
 *   again automatically for SAME_PAGE_MS; the user is asked instead.
 * - Rate: one automatic payment per site every SITE_RATE_MS.
 * - Same invoice: an invoice (by payment hash) that was paid, or is being
 *   paid, is never paid again.
 * - Wrong network or expired invoices are refused.
 *
 * Clocks. The system clock can be moved by the user (or by anyone who can
 * change it), so time is never taken from it alone:
 * - `hw` is the highest wall-clock time ever seen. Time used for any
 *   cooldown is max(now, hw), so moving the clock back never shortens a
 *   cooldown; it stalls it.
 * - If now is clearly before `hw`, the clock was moved back: automatic
 *   payments stop and the user is asked each time until it catches up.
 * - While the same service worker is alive, elapsed time is the smaller of
 *   the wall-clock and the monotonic (performance.now) elapsed time, so a
 *   forward jump cannot end a cooldown early.
 * - Across a service-worker restart the monotonic clock is gone: only the
 *   persisted wall-clock time is left, so a forward jump then ends the
 *   cooldowns (see "Residual risk" in the README).
 */

export const SAME_PAGE_MS = 60_000;
export const SITE_RATE_MS = 5_000;
/** Small corrections (network time sync) are not a rollback. */
export const CLOCK_SLACK_MS = 30_000;
const KEEP_MS = 10 * 60_000;
const MAX_ENTRIES = 200;

/** A moment on both clocks, and which service-worker run the monotonic one belongs to. */
export type Stamp = { wall: number; mono: number; boot: string };

export type GuardState = {
  /** Highest wall-clock time (ms) ever seen. Never goes down. */
  hw: number;
  /** Site -> its last payment, for the rate limit. */
  sites: Record<string, Stamp>;
  /** Page (site + path) -> its last payment. */
  pages: Record<string, Stamp>;
  /** Payment hash -> when it was paid or started. Kept until the list is full, oldest first out. */
  hashes: Record<string, Stamp>;
};

export const emptyGuard = (): GuardState => ({ hw: 0, sites: {}, pages: {}, hashes: {} });

const BOOT = crypto.randomUUID();

export function readClock(): Stamp {
  return { wall: Date.now(), mono: performance.now(), boot: BOOT };
}

/** The wall-clock time to use: never earlier than anything seen before. */
export function trustedWall(g: GuardState, c: Stamp): number {
  return Math.max(c.wall, g.hw);
}

export function clockWentBack(g: GuardState, c: Stamp): boolean {
  return c.wall < g.hw - CLOCK_SLACK_MS;
}

function stampNow(g: GuardState, c: Stamp): Stamp {
  return { wall: trustedWall(g, c), mono: c.mono, boot: c.boot };
}

export function elapsedSince(s: Stamp, g: GuardState, c: Stamp): number {
  const byWall = trustedWall(g, c) - s.wall;
  const e = s.boot === c.boot ? Math.min(byWall, c.mono - s.mono) : byWall;
  return Math.max(0, e);
}

/** The page a payment came from: the site and the path (no query, no subdomain). */
export function pageId(site: string, pageUrl: string | undefined): string {
  try {
    return site + (pageUrl ? new URL(pageUrl).pathname : "/");
  } catch {
    return site + "/";
  }
}

/** Reasons to refuse an invoice outright. Applies even after the user said yes. */
export function checkInvoice(info: Bolt11Info, network: Network, g: GuardState, c: Stamp): string | undefined {
  if (!invoiceMatchesNetwork(info.prefix, network)) {
    return `This invoice is for another Bitcoin network than your wallet (${network}).`;
  }
  // Hygiene, not a cap: the network refuses an expired invoice anyway. It follows the clock as it is now,
  // so a clock set far ahead makes invoices look expired (a safe, visible failure).
  if (invoiceExpired(info, c.wall)) return "This invoice has expired (or the clock of this computer is ahead).";
  if (g.hashes[info.paymentHash]) return "This invoice was already paid, or is being paid. It is not paid twice.";
  return undefined;
}

/** Reasons to ask the user instead of paying by itself. */
export function checkAuto(g: GuardState, c: Stamp, site: string, page: string): { cause: ConfirmCause; reason: string } | undefined {
  if (clockWentBack(g, c)) {
    return {
      cause: "clock_rollback",
      reason: "The clock of this computer moved backwards, so automatic payments are paused until it catches up.",
    };
  }
  const lastPage = g.pages[page];
  if (lastPage && elapsedSince(lastPage, g, c) < SAME_PAGE_MS) {
    return { cause: "same_page", reason: "This page was paid a moment ago and asks again." };
  }
  const lastSite = g.sites[site];
  if (lastSite && elapsedSince(lastSite, g, c) < SITE_RATE_MS) {
    return { cause: "rate_limit", reason: `${site} is asking for payments too fast.` };
  }
  return undefined;
}

/** A payment starts: remember the site, the page and the invoice. */
export function recordPayment(g: GuardState, c: Stamp, p: { site: string; page: string; hash: string }): GuardState {
  const at = stampNow(g, c);
  return prune({
    hw: at.wall,
    sites: { ...g.sites, [p.site]: at },
    pages: { ...g.pages, [p.page]: at },
    hashes: { ...g.hashes, [p.hash]: at },
  });
}

/** The payment settled: the page's quiet time starts now. */
export function recordSettled(g: GuardState, c: Stamp, page: string | undefined): GuardState {
  if (!page) return g;
  const at = stampNow(g, c);
  return prune({ ...g, hw: at.wall, pages: { ...g.pages, [page]: at } });
}

/** The payment failed for certain: the invoice and the page are free again. The site's rate limit stays. */
export function recordFailed(g: GuardState, page: string | undefined, hash: string | undefined): GuardState {
  const pages = { ...g.pages };
  const hashes = { ...g.hashes };
  if (page) delete pages[page];
  if (hash) delete hashes[hash];
  return { ...g, pages, hashes };
}

function prune(g: GuardState): GuardState {
  const newest = (m: Record<string, Stamp>, keep: (s: Stamp) => boolean) =>
    Object.fromEntries(
      Object.entries(m)
        .filter(([, s]) => keep(s))
        .sort(([, a], [, b]) => b.wall - a.wall)
        .slice(0, MAX_ENTRIES),
    );
  const recent = (s: Stamp) => g.hw - s.wall <= KEEP_MS;
  return { hw: g.hw, sites: newest(g.sites, recent), pages: newest(g.pages, recent), hashes: newest(g.hashes, () => true) };
}
