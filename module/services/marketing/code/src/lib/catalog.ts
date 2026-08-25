import { z } from "zod";
import { marketingEnvironment } from "@/config/environment";
import { siteConfig } from "@/config/site";

const int64 = z.preprocess(
  (value) => (typeof value === "string" && /^-?\d+$/.test(value) ? Number(value) : value),
  z.number().int().safe(),
);

const entitlementSchema = z
  .object({
    key: z.string().regex(/^[a-z][a-z0-9_]{0,63}$/),
    limit: int64,
  })
  .strict();

const publicPlanSchema = z
  .object({
    key: z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/),
    name: z.string().min(1).max(80),
    description: z.string().min(1).max(240),
    currency: z.string().regex(/^[A-Z]{3}$/),
    amountMinor: z.preprocess(
      (value) =>
        typeof value === "string" && /^\d+$/.test(value) ? Number(value) : value,
      z.number().int().safe().nonnegative(),
    ).default(0),
    interval: z.enum(["month", "year", "one_time", "contact"]),
    checkoutEnabled: z.boolean().default(false),
    contactSales: z.boolean().default(false),
    trialDays: z.number().int().min(0).max(730).default(0),
    taxBehavior: z.enum(["unspecified", "inclusive", "exclusive", "automatic"]),
    fixture: z.boolean().default(false),
    entitlements: z.array(entitlementSchema).default([]),
  })
  .strict();

const publicCatalogSchema = z
  .object({
    revision: z.string().min(1).max(160),
    plans: z.array(publicPlanSchema),
  })
  .strict();

export type PublicPlan = z.infer<typeof publicPlanSchema>;
export type PublicCatalog = z.infer<typeof publicCatalogSchema>;

export type CatalogState =
  | { kind: "available"; catalog: PublicCatalog }
  | { kind: "disabled" }
  | { kind: "unavailable" };

export type CatalogDisplayState =
  | CatalogState
  | { kind: "empty"; revision: string };

export function parsePublicCatalog(value: unknown): PublicCatalog {
  return publicCatalogSchema.parse(value);
}

export function catalogDisplayState(state: CatalogState): CatalogDisplayState {
  if (state.kind === "available" && state.catalog.plans.length === 0) {
    return { kind: "empty", revision: state.catalog.revision };
  }
  return state;
}

export async function loadPublicPlans(): Promise<CatalogState> {
  const environment = marketingEnvironment();
  if (!siteConfig.pricing.visible || !environment.catalogURL) {
    return { kind: "disabled" };
  }
  const url = new URL(siteConfig.pricing.catalogPath, environment.catalogURL);
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), 1800);
  try {
    const response = await fetch(url, {
      cache: "force-cache",
      credentials: "omit",
      headers: { Accept: "application/json" },
      next: { revalidate: 300, tags: ["public-plan-catalog"] },
      signal: abort.signal,
    });
    if (!response.ok) return { kind: "unavailable" };
    return { kind: "available", catalog: parsePublicCatalog(await response.json()) };
  } catch {
    return { kind: "unavailable" };
  } finally {
    clearTimeout(timer);
  }
}

export function formatPlanPrice(
  plan: PublicPlan,
  locale: string,
): {
  label: string;
  interval: Exclude<PublicPlan["interval"], "contact"> | null;
} {
  if (
    plan.contactSales ||
    plan.interval === "contact" ||
    (!plan.checkoutEnabled && plan.amountMinor > 0)
  ) {
    return { label: "Contact sales", interval: null };
  }
  if (plan.amountMinor === 0) return { label: "Free", interval: null };
  const formatter = new Intl.NumberFormat(locale, {
    style: "currency",
    currency: plan.currency,
  });
  const minorUnitDigits = formatter.resolvedOptions().maximumFractionDigits!;
  return {
    label: formatter.format(plan.amountMinor / 10 ** minorUnitDigits),
    interval: plan.interval,
  };
}
