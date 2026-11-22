/**
 * What the user decided about a site (see site.ts for how sites are keyed).
 * A trusted site may be paid automatically, up to dailySats a day. A site
 * the user turned down stays on the list (not trusted) so it does not ask
 * again; removing it from the list makes it ask again.
 */
export type SiteRule = { trusted: boolean; dailySats: number; since: string };

export type BudgetSettings = {
  /**
   * Usual cap for one page payment (invoice amount plus max fees).
   * If a page costs more than this, the extension asks before paying.
   * A per-site allowance can raise this for one site.
   */
  maxSatsPerPayment: number;
  /** The daily cap offered when the user trusts a new site. */
  maxSatsPerSitePerDay: number;
  /** Daily cap across every site; going over asks for confirmation. */
  maxSatsPerDay: number;
  /**
   * Per-site raised per-page caps, set when the user chooses
   * "always allow this price on this site".
   */
  siteAllowances: Record<string, number>;
  /** Sites the user trusted or turned down, by site key. */
  sites: Record<string, SiteRule>;
};

export const defaultBudget: BudgetSettings = {
  maxSatsPerPayment: 10,
  maxSatsPerSitePerDay: 100,
  maxSatsPerDay: 500,
  siteAllowances: {},
  sites: {},
};

export type SpendState = {
  day: string; // local calendar day, YYYY-MM-DD
  globalSats: number;
  perSite: Record<string, number>; // by site key
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

/** Effective per-page cap for a site, including any "always allow" allowance. */
export function effectivePaymentCap(settings: BudgetSettings, site: string): number {
  const extra = settings.siteAllowances[site] ?? 0;
  return Math.max(settings.maxSatsPerPayment, extra);
}

/**
 * A site the user has not trusted is refused. For a trusted site: if the
 * page price (amount + fees) fits the per-page cap and both daily caps, pay
 * with no prompt. Otherwise ask. Amountless and invalid invoices are still
 * refused. `site` is the site key (see site.ts), not the origin.
 */
export function decidePayment(input: {
  amountSats: number | null;
  site: string;
  settings: BudgetSettings;
  spend: SpendState;
  today: string;
  /** Max routing / SSP fee we will accept; counted in every cap. */
  feeSats?: number;
}): Decision {
  const { amountSats, site, settings } = input;
  const feeSats = input.feeSats ?? 0;
  const rule = settings.sites[site];
  if (!rule?.trusted) {
    return { action: "refuse", reason: "This site is not allowed to use your wallet." };
  }
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
  const cap = effectivePaymentCap(settings, site);
  if (cost > cap) {
    return {
      action: "confirm",
      cause: "over_payment_cap",
      reason: `That is more than your limit for one page (${cap} sats, fee included).`,
    };
  }

  const spend = spendForToday(input.spend, input.today);
  const spent = spend.perSite[site] ?? 0;
  if (spent + cost > rule.dailySats) {
    return {
      action: "confirm",
      cause: "over_site_day",
      reason: `Paying would bring today's spending on ${site} to ${spent + cost} sats at most, fees included. Your daily limit for this site is ${rule.dailySats} sats.`,
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

/** Remember a higher per-page cap for one site. */
export function withSiteAllowance(settings: BudgetSettings, site: string, costSats: number): BudgetSettings {
  if (!Number.isInteger(costSats) || costSats < 1) return settings;
  const current = settings.siteAllowances[site] ?? 0;
  const next = { ...settings.siteAllowances, [site]: Math.max(current, costSats) };
  const keys = Object.keys(next);
  if (keys.length > ALLOWANCE_MAX_SITES && !(site in settings.siteAllowances)) {
    const drop = keys.find((k) => k !== site);
    if (drop) delete next[drop];
  }
  return { ...settings, siteAllowances: next };
}

const SITES_MAX = 200;

/**
 * Record the user's decision about a site. When the list is full, the
 * oldest turned-down site is forgotten to make room; trusted sites are never
 * dropped to make room (if there is nothing to drop, nothing is added).
 */
export function withSiteRule<T extends BudgetSettings>(settings: T, site: string, rule: SiteRule): T {
  const sites = { ...settings.sites };
  if (!(site in sites) && Object.keys(sites).length >= SITES_MAX) {
    const oldest = Object.entries(sites)
      .filter(([, r]) => !r.trusted)
      .sort(([, a], [, b]) => a.since.localeCompare(b.since))[0];
    if (!oldest) return settings;
    delete sites[oldest[0]];
  }
  sites[site] = rule;
  return { ...settings, sites };
}

/** Forget what the user decided about a site (a trusted site is no longer trusted). */
export function withoutSite<T extends BudgetSettings>(settings: T, site: string): T {
  const sites = { ...settings.sites };
  const siteAllowances = { ...settings.siteAllowances };
  delete sites[site];
  delete siteAllowances[site];
  return { ...settings, sites, siteAllowances };
}

/**
 * Change today's counters by delta sats (negative to release). Does nothing
 * if the budget day has moved on: that day's counters are already gone.
 */
export function adjustSpend(spend: SpendState, day: string, site: string, delta: number): SpendState {
  if (spend.day !== day || delta === 0) return spend;
  return {
    day,
    globalSats: Math.max(0, spend.globalSats + delta),
    perSite: { ...spend.perSite, [site]: Math.max(0, (spend.perSite[site] ?? 0) + delta) },
  };
}

export function recordSpend(spend: SpendState, today: string, site: string, amountSats: number): SpendState {
  const base = spendForToday(spend, today);
  return {
    day: today,
    globalSats: base.globalSats + amountSats,
    perSite: { ...base.perSite, [site]: (base.perSite[site] ?? 0) + amountSats },
  };
}
