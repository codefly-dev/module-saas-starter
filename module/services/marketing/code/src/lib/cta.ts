import { acquisitionMode, appOrigin, siteConfig } from "@/config/site";

export type AttributionInput = Record<
  string,
  string | string[] | undefined
>;

function first(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value;
}

export function approvedAttribution(input: AttributionInput): URLSearchParams {
  const output = new URLSearchParams();
  for (const field of siteConfig.acquisition.allowedAttribution) {
    const value = first(input[field])?.trim();
    if (!value) continue;
    output.set(field, value.slice(0, 200));
  }
  return output;
}

export function productHandoff(
  destination: "login" | "signup" | "waitlist",
  attribution: AttributionInput = {},
  plan?: string,
): string | null {
  const configuredPath =
    destination === "login"
      ? siteConfig.acquisition.loginPath
      : destination === "signup"
        ? siteConfig.acquisition.signupPath
        : siteConfig.acquisition.waitlistPath;
  if (!configuredPath) return null;
  const url = new URL(configuredPath, appOrigin);
  const allowed = approvedAttribution(attribution);
  for (const [key, value] of allowed) url.searchParams.set(key, value);
  if (plan && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(plan)) {
    url.searchParams.set("plan", plan);
  }
  return url.toString();
}

export function primaryAcquisitionHandoff(
  attribution: AttributionInput = {},
): { href: string | null; label: string; available: boolean } {
  switch (acquisitionMode()) {
    case "open_signup":
      return {
        href: productHandoff("signup", attribution),
        label: "Start building",
        available: true,
      };
    case "waitlist": {
      const href = productHandoff("waitlist", attribution);
      return { href, label: "Join the waitlist", available: href !== null };
    }
    case "request_demo":
      return {
        href: `mailto:${siteConfig.contacts.sales}?subject=Product%20demo`,
        label: "Request a demo",
        available: !siteConfig.contacts.sales.endsWith(".invalid"),
      };
    case "invite_only":
      return {
        href: productHandoff("login", attribution),
        label: "Sign in with an invitation",
        available: true,
      };
  }
  throw new Error("Unsupported acquisition mode");
}
