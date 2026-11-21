export type BudgetSettings = {
  /**
   * Usual cap for one page payment (invoice amount plus max fees).
   * If a page costs more than this, the extension asks before paying.
   * A per-site allowance can raise this for one origin.
   */
  maxSatsPerPayment: number;
  /** Daily cap for one site origin; going over asks for confirmation. */
  maxSatsPerSitePerDay: number;
  /** Daily cap across every site; going over asks for confirmation. */
  maxSatsPerDay: number;
  /**
   * Per-origin raised per-page caps, set when the user chooses
   * "always allow this price on this site".
   */
  siteAllowances: Record<string, number>;
};

export const defaultBudget: BudgetSettings = {
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  siteAllowances: {},
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

export type ConfirmCause = "over_payment_cap" | "over_site_day" | "over_global_day";

export type Decision =
  | { action: "refuse"; reason: string }
  | { action: "pay" }
  | { action: "confirm"; reason: string; cause: ConfirmCause };

export type ConfirmChoice = "once" | "always" | "refuse";

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

/** Invoice amount plus the fee we are willing to pay. */
export function paymentCost(amountSats: number, feeSats: number): number {
  if (!Number.isSafeInteger(amountSats) || !Number.isSafeInteger(feeSats) || amountSats < 0 || feeSats < 0) {
    throw new Error("Payment cost is invalid.");
  }
  const total = amountSats + feeSats;
  if (!Number.isSafeInteger(total)) throw new Error("Payment cost is too large.");
  return total;
}

/** Effective per-page cap for an origin, including any "always allow" allowance. */
export function effectivePaymentCap(settings: BudgetSettings, origin: string): number {
  const extra = settings.siteAllowances[origin] ?? 0;
  return Math.max(settings.maxSatsPerPayment, extra);
}

/**
 * If the page price (amount + fees) fits the per-page cap and both daily
 * caps, pay with no prompt. Otherwise ask. Amountless and invalid invoices
 * are still refused.
 */
export function decidePayment(input: {
  amountSats: number | null;
  origin: string;
  settings: BudgetSettings;
  spend: SpendState;
  today: string;
  /** Max routing / SSP fee we will accept; counted in every cap. */
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

  const cost = paymentCost(amountSats, feeSats);
  const cap = effectivePaymentCap(settings, origin);
  if (cost > cap) {
    return {
      action: "confirm",
      cause: "over_payment_cap",
      reason: `That is more than your limit for one page (${cap} sats, fee included).`,
    };
  }

  const spend = spendForToday(input.spend, input.today);
  const site = spend.perSite[origin] ?? 0;
  if (site + cost > settings.maxSatsPerSitePerDay) {
    return {
      action: "confirm",
      cause: "over_site_day",
      reason: `Paying would bring today's spending on ${origin} to ${site + cost} sats at most, fees included. Your daily limit for this site is ${settings.maxSatsPerSitePerDay} sats.`,
    };
  }
  if (spend.globalSats + cost > settings.maxSatsPerDay) {
    return {
      action: "confirm",
      cause: "over_global_day",
      reason: `Paying would bring today's total spending to ${spend.globalSats + cost} sats at most, fees included. Your daily limit is ${settings.maxSatsPerDay} sats.`,
    };
  }
  return { action: "pay" };
}

const ALLOWANCE_MAX_SITES = 100;

/** Remember a higher per-page cap for one origin. */
export function withSiteAllowance(settings: BudgetSettings, origin: string, costSats: number): BudgetSettings {
  if (!Number.isInteger(costSats) || costSats < 1) return settings;
  const current = settings.siteAllowances[origin] ?? 0;
  const next = { ...settings.siteAllowances, [origin]: Math.max(current, costSats) };
  const keys = Object.keys(next);
  if (keys.length > ALLOWANCE_MAX_SITES && !(origin in settings.siteAllowances)) {
    const drop = keys.find((k) => k !== origin);
    if (drop) delete next[drop];
  }
  return { ...settings, siteAllowances: next };
}

/**
 * Change today's counters by delta sats (negative to release). Does nothing
 * if the budget day has moved on: that day's counters are already gone.
 */
export function adjustSpend(spend: SpendState, day: string, origin: string, delta: number): SpendState {
  if (spend.day !== day || delta === 0) return spend;
  return {
    day,
    globalSats: Math.max(0, spend.globalSats + delta),
    perSite: { ...spend.perSite, [origin]: Math.max(0, (spend.perSite[origin] ?? 0) + delta) },
  };
}

export function recordSpend(spend: SpendState, today: string, origin: string, amountSats: number): SpendState {
  const base = spendForToday(spend, today);
  return {
    day: today,
    globalSats: base.globalSats + amountSats,
    perSite: { ...base.perSite, [origin]: (base.perSite[origin] ?? 0) + amountSats },
  };
}
