// Pricing plan definitions — pure TypeScript, no React, no fetch.
// The canonical prices live in Stripe and are synced via the backend
// plans table. This file just describes what we SHOW on the pricing
// page. Keep it in lockstep with the plans row display_name / features.

export interface PricingPlan {
  name: string; // backend plans.name (matches stripe mapping)
  displayName: string;
  priceLabel: string; // what the user sees
  description: string;
  features: string[];
  cta: "start_free" | "upgrade" | "contact";
  highlighted?: boolean;
}

export const PRICING_PLANS: PricingPlan[] = [
  {
    name: "free",
    displayName: "Free",
    priceLabel: "$0",
    description: "For individuals exploring the product.",
    features: ["5 team seats", "2 API keys", "10k API calls / month"],
    cta: "start_free",
  },
  {
    name: "pro",
    displayName: "Pro",
    priceLabel: "$29 / month",
    description: "For growing teams shipping real product.",
    features: [
      "50 team seats",
      "20 API keys",
      "500k API calls / month",
      "SSO login",
      "Audit log",
    ],
    cta: "upgrade",
    highlighted: true,
  },
  {
    name: "enterprise",
    displayName: "Enterprise",
    priceLabel: "Custom",
    description: "For organizations with custom needs.",
    features: [
      "Unlimited seats",
      "Unlimited API keys",
      "Unlimited API calls",
      "SSO + SCIM",
      "Audit log + retention",
      "SLA + dedicated support",
    ],
    cta: "contact",
  },
];
