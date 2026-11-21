import type { PaymentRecord } from "../types";

/** Just the site name, never the full URL. */
export function siteHost(origin: string): string {
  if (origin === "popup") return "This wallet";
  try {
    return new URL(origin).host || origin;
  } catch {
    return origin.slice(0, 40);
  }
}

export type HistoryRow = {
  site: string;
  time: string;
  /** What was paid, as short pieces for one compact line. */
  status: "paid" | "failed" | "declined" | "waiting";
  price: number;
  fee?: number;
  total: number;
};

/** One compact row per payment: site, time, price, fee, total. Nothing from the invoice or preimage. */
export function historyRow(r: PaymentRecord, locale?: string): HistoryRow {
  const price = r.priceSats ?? r.amountSats;
  const paid = r.result === "sent";
  const fee = paid ? r.feeSats : undefined;
  return {
    site: siteHost(r.origin),
    time: new Date(r.time).toLocaleString(locale, { dateStyle: "short", timeStyle: "short" }),
    status: paid ? "paid" : r.result === "failed" ? "failed" : r.result === "refused" ? "declined" : "waiting",
    price,
    ...(fee === undefined ? {} : { fee }),
    total: paid ? price + (fee ?? 0) : price,
  };
}
