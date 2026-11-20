export type BudgetSettings = {
  /** Hard cap for a single page payment. Above this, the payment is refused. */
  maxSatsPerPayment: number;
  /** Soft daily cap for one site origin; going over asks for confirmation. */
  maxSatsPerSitePerDay: number;
  /** Soft daily cap across every site; going over asks for confirmation. */
  maxSatsPerDay: number;
  /** If false, every payment asks for confirmation (still refuses hard caps). */
  autoPay: boolean;
};

export const defaultBudget: BudgetSettings = {
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  autoPay: true,
};

export type SpendState = {
  day: string; // local calendar day, YYYY-MM-DD
  globalSats: number;
  perSite: Record<string, number>;
};

export const emptySpend = (day: string): SpendState => ({
  day,
  globalSats: 0,
  perSite: {},
});

export type Decision =
  | { action: "refuse"; reason: string }
  | { action: "pay" }
  | { action: "confirm"; reason: string };

export function todayLocal(now = new Date()): string {
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

/** Spend figures for today (resets if the stored day is not today). */
export function spendForToday(stored: SpendState | undefined, today: string): SpendState {
  if (!stored || stored.day !== today) return emptySpend(today);
  return stored;
}

/** Invoice amount plus the fee we are willing to pay, used for daily caps. */
export function paymentCost(amountSats: number, feeSats: number): number {
  if (!Number.isSafeInteger(amountSats) || !Number.isSafeInteger(feeSats) || amountSats < 0 || feeSats < 0) {
    throw new Error("Payment cost is invalid.");
  }
  const total = amountSats + feeSats;
  if (!Number.isSafeInteger(total)) throw new Error("Payment cost is too large.");
  return total;
}

export function decidePayment(input: {
  amountSats: number | null;
  origin: string;
  settings: BudgetSettings;
  spend: SpendState;
  today: string;
  /** Max routing / SSP fee we will accept; counted against daily caps, not the per-payment cap. */
  feeSats?: number;
}): Decision {
  const { amountSats, origin, settings } = input;
  const feeSats = input.feeSats ?? 0;
  if (amountSats === null) {
    return { action: "refuse", reason: "Amountless invoices are not paid automatically." };
  }
  if (!Number.isInteger(amountSats) || amountSats < 1) {
    return { action: "refuse", reason: "Invoice amount is invalid." };
  }
  if (!Number.isInteger(feeSats) || feeSats < 0) {
    return { action: "refuse", reason: "Fee is invalid." };
  }
  if (amountSats > settings.maxSatsPerPayment) {
    return {
      action: "refuse",
      reason: `This invoice is ${amountSats} sats, above the per-payment cap of ${settings.maxSatsPerPayment} sats.`,
    };
  }

  const spend = spendForToday(input.spend, input.today);
  const site = spend.perSite[origin] ?? 0;
  const cost = paymentCost(amountSats, feeSats);
  if (site + cost > settings.maxSatsPerSitePerDay) {
    return {
      action: "confirm",
      reason: `This would take today's spend on ${origin} to ${site + cost} sats, including up to ${feeSats} sats of fees (limit ${settings.maxSatsPerSitePerDay}).`,
    };
  }
  if (spend.globalSats + cost > settings.maxSatsPerDay) {
    return {
      action: "confirm",
      reason: `This would take today's total spend to ${spend.globalSats + cost} sats, including up to ${feeSats} sats of fees (limit ${settings.maxSatsPerDay}).`,
    };
  }
  if (!settings.autoPay) {
    return { action: "confirm", reason: "Auto-pay is off. Confirm this payment?" };
  }
  return { action: "pay" };
}

export function recordSpend(spend: SpendState, today: string, origin: string, amountSats: number): SpendState {
  const base = spendForToday(spend, today);
  return {
    day: today,
    globalSats: base.globalSats + amountSats,
    perSite: { ...base.perSite, [origin]: (base.perSite[origin] ?? 0) + amountSats },
  };
}
